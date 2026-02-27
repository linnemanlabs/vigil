package postgres

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/linnemanlabs/go-core/log"
)

var queryObserver atomic.Pointer[queryObserverHolder]

const (
	ctxKeySQL        ctxKey = "pgx.sql"
	ctxKeyArgs       ctxKey = "pgx.args"
	ctxKeyStart      ctxKey = "pgx.start"
	ctxKeyCaller     ctxKey = "db.caller"
	ctxKeyHandler    ctxKey = "db.handler"
	ctxKeyHTTPMethod ctxKey = "http.method"
)

// minQueryLogDuration controls the threshold for logging queries.
// 0 means log all queries.
const minQueryLogDuration = 0 * time.Millisecond

// context keys for query metadata.
type ctxKey string

type dbStatsKey struct{}

type queryObserverHolder struct{ QueryObserver }

// ReqDBStats accumulates per-request database query statistics.
type ReqDBStats struct {
	mu            sync.Mutex
	QueryCount    int
	TotalDuration time.Duration
	ErrorCount    int
}

// loggingTracer wraps another pgx.QueryTracer (e.g. otelpgx)
// and adds a structured log line for every query.
type loggingTracer struct {
	inner pgx.QueryTracer
}

// QueryObserver receives per-query metrics (wired by main for Prometheus).
type QueryObserver interface {
	ObserveQuery(ctx context.Context, method, route, outcome string, dur time.Duration)
}

// QueryObserverFunc adapts a plain function to QueryObserver.
type QueryObserverFunc func(ctx context.Context, method, route, outcome string, dur time.Duration)

// ObserveQuery implements QueryObserver.
func (f QueryObserverFunc) ObserveQuery(ctx context.Context, method, route, outcome string, dur time.Duration) {
	f(ctx, method, route, outcome, dur)
}

// AddQuery records a single query execution.
func (s *ReqDBStats) AddQuery(dur time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.QueryCount++
	s.TotalDuration += dur
	if err != nil {
		s.ErrorCount++
	}
}

// SetQueryObserver sets the global query observer (typically a Prometheus histogram).
func SetQueryObserver(o QueryObserver) {
	if o == nil {
		queryObserver.Store(nil)
		return
	}
	queryObserver.Store(&queryObserverHolder{QueryObserver: o})
}

// WithHTTPMethod stores the HTTP method in the context for query metrics labelling.
func WithHTTPMethod(ctx context.Context, method string) context.Context {
	if method == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyHTTPMethod, method)
}

// NewReqDBStatsContext returns a new context with an empty ReqDBStats attached.
func NewReqDBStatsContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, dbStatsKey{}, &ReqDBStats{})
}

// ReqDBStatsFromContext extracts the ReqDBStats from the context, if present.
func ReqDBStatsFromContext(ctx context.Context) (*ReqDBStats, bool) {
	s, ok := ctx.Value(dbStatsKey{}).(*ReqDBStats)
	return s, ok
}

func getQueryObserver() QueryObserver {
	h := queryObserver.Load()
	if h == nil {
		return nil
	}
	return h.QueryObserver
}

func httpMethodFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyHTTPMethod).(string); ok {
		return v
	}
	return ""
}

func routePatternFromContext(ctx context.Context) string {
	if rc := chi.RouteContext(ctx); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}

// wrapQueryTracer wraps an inner tracer with structured logging.
func wrapQueryTracer(inner pgx.QueryTracer) pgx.QueryTracer {
	if inner == nil {
		return loggingTracer{}
	}
	return loggingTracer{inner: inner}
}

func (t loggingTracer) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	sql := data.SQL
	args := data.Args
	start := time.Now()

	// Compute caller/handler based on the *app* call stack, once per query.
	caller, handler := findDBCallerAndHandler()

	// Let inner tracer (otelpgx) create its span first.
	if t.inner != nil {
		ctx = t.inner.TraceQueryStart(ctx, conn, data)
	}

	// Stash data into context for TraceQueryEnd.
	ctx = context.WithValue(ctx, ctxKeySQL, sql)
	ctx = context.WithValue(ctx, ctxKeyArgs, args)
	ctx = context.WithValue(ctx, ctxKeyStart, start)
	if caller != "" {
		ctx = context.WithValue(ctx, ctxKeyCaller, caller)
	}
	if handler != "" {
		ctx = context.WithValue(ctx, ctxKeyHandler, handler)
	}

	// Annotate DB span with caller/handler so they show up on the DB span itself.
	if span := trace.SpanFromContext(ctx); span != nil && span.IsRecording() {
		attrs := make([]attribute.KeyValue, 0, 2)
		if caller != "" {
			attrs = append(attrs, attribute.String("db.caller", caller))
		}
		if handler != "" {
			attrs = append(attrs, attribute.String("db.handler", handler))
		}
		if len(attrs) > 0 {
			span.SetAttributes(attrs...)
		}
	}

	return ctx
}

func (t loggingTracer) TraceQueryEnd(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryEndData,
) {
	// Always call inner tracer first so spans are finished correctly.
	if t.inner != nil {
		t.inner.TraceQueryEnd(ctx, conn, data)
	}

	sql, _ := ctx.Value(ctxKeySQL).(string)
	args, _ := ctx.Value(ctxKeyArgs).([]any)
	start, _ := ctx.Value(ctxKeyStart).(time.Time)
	caller, _ := ctx.Value(ctxKeyCaller).(string)
	handler, _ := ctx.Value(ctxKeyHandler).(string)

	var dur time.Duration
	if !start.IsZero() {
		dur = time.Since(start)
	}

	// Append query time to per-request DB stats.
	if s, ok := ReqDBStatsFromContext(ctx); ok {
		s.AddQuery(dur, data.Err)
	}

	// Metrics hook (runs for every query, not just ones we log).
	if obs := getQueryObserver(); obs != nil && dur > 0 {
		method := httpMethodFromContext(ctx)
		if method == "" {
			method = "UNKNOWN"
		}

		route := routePatternFromContext(ctx)
		if route == "" {
			route = "unknown"
		}

		outcome := "ok"
		if data.Err != nil {
			outcome = "error"
		}
		obs.ObserveQuery(ctx, method, route, outcome, dur)
	}

	// Don't log if query duration < minQueryLogDuration.
	if minQueryLogDuration > 0 && dur < minQueryLogDuration && data.Err == nil {
		return
	}

	L := log.FromContext(ctx)

	fields := []any{
		"db.statement", sql,
		"db.args", args,
		"db.duration", dur.Seconds(),
	}

	// Derive operation name & keep full command tag.
	tag := strings.TrimSpace(data.CommandTag.String())
	if tag != "" {
		parts := strings.Fields(tag)
		if len(parts) > 0 {
			fields = append(fields, "db.operation.name", strings.ToUpper(parts[0]))
		}
		fields = append(fields, "pg.command_tag", tag)

		// Rows affected also comes from CommandTag.
		if rows := data.CommandTag.RowsAffected(); rows >= 0 {
			fields = append(fields, "db.rows", rows)
		}
	}

	if caller != "" {
		fields = append(fields, "db.caller", caller)
	}
	if handler != "" {
		fields = append(fields, "db.handler", handler)
	}

	// PG error details.
	if data.Err != nil {
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) {
			fields = append(fields,
				"db.error_code", pgErr.Code,
				"db.error_constraint", pgErr.ConstraintName,
			)
		}
	}

	if data.Err != nil {
		L.Error(ctx, data.Err, "db query failed", fields...)
	} else {
		L.Info(ctx, "db query", fields...)
	}
}

// findDBCallerAndHandler walks the stack to find:
//   - caller: the repo or low-level function actually issuing the query
//   - handler: the next meaningful frame above that (e.g. service/handler)
func findDBCallerAndHandler() (caller, handler string) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])

	gotCaller := false

	for {
		fr, more := frames.Next()
		if !more {
			break
		}

		fn := fr.Function

		// Skip noise: runtime, pgx internals, otelpgx, tracer itself.
		if strings.HasPrefix(fn, "runtime.") ||
			strings.Contains(fn, "github.com/jackc/pgx/v5") ||
			strings.Contains(fn, "github.com/exaring/otelpgx") ||
			strings.Contains(fn, "loggingTracer.TraceQuery") {
			continue
		}

		short := shortenFuncName(fn)

		if !gotCaller {
			caller = short
			gotCaller = true
			continue
		}

		// For handler, skip repo-level helpers.
		if strings.Contains(fn, "github.com/linnemanlabs/vigil/internal/postgres.") {
			continue
		}

		handler = short
		break
	}

	return caller, handler
}

func shortenFuncName(fn string) string {
	// Trim package path.
	if i := strings.LastIndex(fn, "/"); i >= 0 && i+1 < len(fn) {
		fn = fn[i+1:]
	}
	// Trim module path, keep receiver + method.
	if dot := strings.Index(fn, "."); dot >= 0 && dot+1 < len(fn) {
		fn = fn[dot+1:]
	}
	return fn
}
