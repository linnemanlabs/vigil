// Vigil is an AI-powered infrastructure alert analysis and triage tool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/linnemanlabs/go-core/cfg"
	"github.com/linnemanlabs/go-core/opshttp"
	"github.com/linnemanlabs/go-core/prof"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/linnemanlabs/go-core/health"

	"github.com/linnemanlabs/go-core/httpmw"
	"github.com/linnemanlabs/go-core/httpserver"

	"github.com/linnemanlabs/go-core/log"

	"github.com/linnemanlabs/go-core/metrics"
	"github.com/linnemanlabs/go-core/otelx"
	v "github.com/linnemanlabs/go-core/version"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/linnemanlabs/vigil/internal/alertapi"
	vc "github.com/linnemanlabs/vigil/internal/cfg"
	"github.com/linnemanlabs/vigil/internal/llm/claude"
	"github.com/linnemanlabs/vigil/internal/notify/slack"
	"github.com/linnemanlabs/vigil/internal/postgres"
	"github.com/linnemanlabs/vigil/internal/tools"
	"github.com/linnemanlabs/vigil/internal/triage"
	"github.com/linnemanlabs/vigil/internal/triage/memstore"
	"github.com/linnemanlabs/vigil/internal/triage/pgstore"
)

const appName = "vigil"
const component = "server"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Set app name and component
	v.AppName = appName
	v.Component = component

	// Get build/version info
	vi := v.Get()

	// each package registers its own flags and options struct
	var (
		appCfg    vc.Config
		httpCfg   httpserver.Config
		httpmwCfg httpmw.Config
		logCfg    log.Config
		opsCfg    opshttp.Config
		profCfg   prof.Config
		traceCfg  otelx.Config
	)

	// register flags for each package, which will be parsed into the shared config struct
	appCfg.RegisterFlags(flag.CommandLine)
	httpCfg.RegisterFlags(flag.CommandLine)
	httpmwCfg.RegisterFlags(flag.CommandLine)
	logCfg.RegisterFlags(flag.CommandLine)
	opsCfg.RegisterFlags(flag.CommandLine)
	profCfg.RegisterFlags(flag.CommandLine)
	traceCfg.RegisterFlags(flag.CommandLine)
	var showVersion bool
	flag.BoolVar(&showVersion, "V", false, "Print version+build information and exit")

	// parse flags to get config values from cmdline, we check env vars next which do not override cmdline flags
	flag.Parse()
	if showVersion {
		fmt.Printf(
			"%s (%s) %s (commit=%s, commit_date=%s, build_id=%s, build_date=%s, go=%s, dirty=%v)\n",
			vi.AppName, vi.Component, vi.Version, vi.Commit, vi.CommitDate, vi.BuildId, vi.BuildDate, vi.GoVersion,
			vi.VCSDirty != nil && *vi.VCSDirty,
		)
		return nil
	}

	// Fill in config values from environment variables with prefix VIGIL_,
	// these do not override cmdline flags
	cfg.FillFromEnv(flag.CommandLine, "VIGIL_", func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})

	if err := errors.Join(
		appCfg.Validate(),
		httpCfg.Validate(),
		httpmwCfg.Validate(),
		logCfg.Validate(),
		opsCfg.Validate(),
		profCfg.Validate(),
		traceCfg.Validate(),
	); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	// cross-cutting checks that only main can validate
	if appCfg.APIPort == opsCfg.Port {
		return fmt.Errorf("http and admin ports must differ (both %d)", appCfg.APIPort)
	}

	// initialize logger early
	lg, err := log.New(logCfg.ToOptions(v.AppName))
	if err != nil {
		return fmt.Errorf("logger init: %w", err)
	}
	// no-op for slog/stderr, but here if we swap backends in the future to ensure any buffered logs are flushed on shutdown
	defer func() { _ = lg.Sync() }()

	// create a logger with component field pre-filled for structured logging in this package
	L := lg.With("component", vi.Component)

	// add logger to context
	ctx = log.WithContext(ctx, L)

	L.Info(ctx, "initializing application",
		"version", vi.Version,
		"commit", vi.Commit,
		"commit_date", vi.CommitDate,
		"build_id", vi.BuildId,
		"build_date", vi.BuildDate,
		"go_version", vi.GoVersion,
		"vcs_dirty", vi.VCSDirty,
		"http_port", appCfg.APIPort,
		"admin_port", opsCfg.Port,
		"enable_pprof", opsCfg.EnablePprof,
		"enable_pyroscope", profCfg.EnablePyroscope,
		"enable_tracing", traceCfg.EnableTracing,
		"trace_sample", traceCfg.TraceSample,
		"trace_insecure", traceCfg.Insecure,
		"otlp_endpoint", traceCfg.OTLPEndpoint,
		"pyro_server", profCfg.PyroServer,
		"pyro_tenant", profCfg.PyroTenantID,
		"include_error_links", logCfg.IncludeErrorLinks,
		"max_error_links", logCfg.MaxErrorLinks,
		"trusted_proxy_hops", httpmwCfg.TrustedProxyHops,
	)

	// Setup pyroscope profiling early so we get profiles from the entire app lifetime
	profOpts := profCfg.ToOptions()
	profOpts.AppName = v.AppName
	profOpts.Tags = map[string]string{
		"app":       v.AppName,
		"component": v.Component,
		"version":   vi.Version,
		"commit":    vi.Commit,
		"build_id":  vi.BuildId,
		"source":    "lmlabs-go-agent",
	}
	// Start profiling, returns a stop function to call for clean shutdown (flush buffers, etc)
	stopProf, profErr := prof.Start(ctx, profOpts)
	if profErr != nil {
		L.Error(ctx, profErr, "pyroscope start failed", "pyro_server", profCfg.PyroServer)
	}
	if stopProf != nil {
		defer stopProf()
	}

	// Setup otel for tracing
	traceOpts := traceCfg.ToOptions()
	traceOpts.Service = v.AppName
	traceOpts.Component = v.Component
	traceOpts.Version = v.Version

	// Start otel, returns a shutdown function to call for clean shutdown (flush buffers, etc)
	shutdownOtelx, err := otelx.Init(ctx, traceOpts)
	if err != nil {
		L.Error(ctx, err, "otel init failed")
	}
	if shutdownOtelx != nil {
		defer func() { _ = shutdownOtelx(context.Background()) }()
	}

	// Setup metrics, we use our own metrics package for internal instrumentation
	var m = metrics.New()
	m.SetBuildInfoFromVersion(v.AppName, "server", &vi)
	m.SetProfilingActive(profErr == nil && profCfg.EnablePyroscope)

	// Initialize the tool registry and register available tools
	registry := tools.NewRegistry()

	// Register Prometheus query tools if endpoint is configured, this allows the triage engine to query metrics for alert investigation and correlation
	if appCfg.PrometheusEndpoint != "" {
		prometheusQuery := tools.NewPrometheusQuery(appCfg.PrometheusEndpoint, appCfg.PrometheusTenantID)
		registry.Register(prometheusQuery)
		L.Info(ctx, "registered tool", "name", prometheusQuery.Name(), "endpoint", appCfg.PrometheusEndpoint)
		prometheusQueryRange := tools.NewPrometheusQueryRange(appCfg.PrometheusEndpoint, appCfg.PrometheusTenantID)
		registry.Register(prometheusQueryRange)
		L.Info(ctx, "registered tool", "name", prometheusQueryRange.Name(), "endpoint", appCfg.PrometheusEndpoint)
	}

	// Register Loki query tool if endpoint is configured, this allows the triage engine to query logs for alert investigation and correlation
	if appCfg.LokiEndpoint != "" {
		lokiQuery := tools.NewLokiQuery(appCfg.LokiEndpoint, appCfg.LokiTenantID)
		registry.Register(lokiQuery)
		L.Info(ctx, "registered tool", "name", lokiQuery.Name(), "endpoint", appCfg.LokiEndpoint)
	}

	// Initialize the triage store
	var triageStore triage.Store
	if appCfg.DatabaseURL != "" {
		pool, err := postgres.NewPool(ctx, appCfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("postgres pool: %w", err)
		}
		defer pool.Close()
		pgStore, err := pgstore.New(ctx, pool)
		if err != nil {
			return fmt.Errorf("pgstore init: %w", err)
		}
		triageStore = pgStore
		L.Info(ctx, "using postgres store")
	} else {
		triageStore = memstore.New()
		L.Info(ctx, "using in-memory store (no database-url configured)")
	}

	// Initialize Claude provider.
	claudeProvider := claude.New(appCfg.ClaudeAPIKey, appCfg.ClaudeModel)
	L.Info(ctx, "initialized LLM provider", "provider", "claude", "model", appCfg.ClaudeModel)
	if claudeProvider == nil {
		return fmt.Errorf("failed to initialize Claude provider")
	}

	// Initialize triage metrics on the shared Prometheus registry.
	triageMetrics := triage.NewMetrics(m.Registry())

	// Register per-query DB duration histogram and wire the observer.
	dbQueryDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vigil_db_query_duration_seconds",
		Help:    "Duration of individual database queries.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "outcome"})
	m.Registry().MustRegister(dbQueryDuration)

	postgres.SetQueryObserver(postgres.QueryObserverFunc(
		func(_ context.Context, method, route, outcome string, dur time.Duration) {
			dbQueryDuration.WithLabelValues(method, route, outcome).Observe(dur.Seconds())
		},
	))

	// Initialize the triage engine (pure - no store dependency).
	claudeEngine := triage.NewEngine(claudeProvider, registry, L, triageMetrics.Hooks())
	if claudeEngine == nil {
		return fmt.Errorf("failed to initialize triage engine for Claude provider")
	}

	// Initialize Slack notifier for triage result notifications.
	var notifier triage.Notifier
	if appCfg.SlackWebhookURL != "" {
		notifier = slack.New(appCfg.SlackWebhookURL, L)
		L.Info(ctx, "notifier enabled", "type", "slack")
	}

	// Initialize the triage service (owns dedup, lifecycle, async dispatch).
	triageSvc := triage.NewService(triageStore, claudeEngine, L, triageMetrics, notifier)

	// setup toggle for server shutdown. this is used to fail readiness checks
	// during shutdown to drain connections from load balancer before killing the process.
	var shutdownGate health.ShutdownGate

	// setup readiness checks, currently just the shutdown gate
	readiness := health.All(
		shutdownGate.Probe(),
	)
	// liveness is always true if the app is able to respond
	liveness := health.Fixed(true, "")

	// Configure ops http server for metrics, health checks, pprof, etc
	opsOpts := opsCfg.ToOptions()
	opsOpts.Metrics = m.Handler()
	opsOpts.Health = liveness
	opsOpts.Readiness = readiness
	opsOpts.UseRecoverMW = true
	opsOpts.OnPanic = m.IncHttpPanic

	// start admin/ops listener. sg restricts inbound to internal monitoring infrastructure.
	// we reject connections from public ips and requests with x-forwarded set in middleware
	// to prevent accidental exposure if sg is misconfigured or load balancer ever sends traffic here
	opsHTTPStop, err := opshttp.Start(ctx, L, opsOpts)
	if err != nil {
		L.Error(ctx, err, "failed to start ops http listener")
		return err
	}
	defer func() {
		err := opsHTTPStop(context.Background())
		if err != nil {
			L.Error(ctx, err, "failed to stop ops http listener")
		}
	}()

	// setup main api chi router and middleware stack
	r := chi.NewRouter()

	// Compress text responses (we are JSON only for now)
	r.Use(middleware.Compress(5, "application/json"))

	// Annotate logger (and tracer if trace is recording) with http.route from chi route pattern
	r.Use(httpmw.AnnotateHTTPRoute)

	// Stash HTTP method in context for DB query metrics labelling.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(postgres.WithHTTPMethod(req.Context(), req.Method)))
		})
	})

	// Access log middleware
	r.Use(httpmw.AccessLog())

	// Limit request body size, this is a wrapper around http.MaxBytesHandler which returns 413 if limit is exceeded
	r.Use(httpmw.MaxBody(1024 * 64)) // 64KB to start with may adjust after i see real traffic

	// add health check endpoints to main listener
	r.Get("/-/healthy", health.HealthzHandler(liveness))
	r.Get("/-/ready", health.ReadyzHandler(readiness))

	// register api routes
	alertapiHTTP := alertapi.New(L, triageSvc)
	alertapiHTTP.RegisterRoutes(r)

	// middleware stack for main listener, order matters these are wrappers, outermost sees raw request
	// first and is last to see response, innermost is last to see request and first to see response but
	// has access to the full rich context from outer middleware and handlers
	var h http.Handler = r

	// Request-scoped logging (inner so it sees trace_id, chi route, etc)
	h = httpmw.WithLogger(L)(h)

	// add trace-id and span-id headers to any requests with a recording trace
	h = httpmw.TraceResponseHeaders("X-Trace-Id", "X-Span-Id")(h)

	// otel instrumentation for automatic spans and trace context propagation
	h = otelhttp.NewHandler(h, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			// dont trace health/readiness checks
			return r.URL.Path != "/-/healthy" && r.URL.Path != "/-/ready"
		}),
		// AnnotateHTTPRoute will rename the span later to the final route pattern
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		// WithPublicEndpointFn is the replacement for WithPublicEndpoint()
		otelhttp.WithPublicEndpointFn(func(_ *http.Request) bool { return true }),
	)

	// Metrics middleware for prometheus instrumentation
	h = m.Middleware(h)

	// Client IP resolution and spoofing protection middleware, outer so downstream middleware
	// and handlers can use the resolved client ip from context for consistency and security
	h = httpmw.ClientIPWithOptions(httpmw.ClientIPOptions{
		TrustedHops: httpmwCfg.TrustedProxyHops,
	})(h)

	// Request ID (outer so everything downstream sees it)
	h = httpmw.RequestID("X-Request-Id")(h) // request ID

	// Recovery middleware to recover and log panics and serve 500 response.
	// Outer to catch panics from any downstream middleware or handlers
	h = httpmw.Recover(L, nil)(h)

	// Security headers outermost to ensure they are served on every response
	h = httpmw.SecurityHeaders(h)

	// Configure http server options from config
	alertapiOpts, err := httpCfg.ToOptions()
	if err != nil {
		L.Error(ctx, err, "invalid http config")
		return err
	}

	// Start alertapi HTTP server with middleware and handlers
	alertapiHTTPStop, err := httpserver.Start(ctx, fmt.Sprintf(":%d", appCfg.APIPort), h, L, alertapiOpts)
	if err != nil {
		L.Error(ctx, err, "failed to start alertapi http listener")
		return err
	}
	defer func() {
		err := alertapiHTTPStop(context.Background())
		if err != nil {
			L.Error(ctx, err, "failed to stop alertapi http listener")
		}
	}()

	// Notify systemd that we started successfully if started under systemd
	if err := notifySystemd(); err != nil {
		// log and dont exit, worst case systemd will kill the process after timeout
		L.Warn(ctx, "failed to notify systemd of readiness", "error", err)
	}

	// Wait for ctrl+c / sigterm
	<-ctx.Done()

	L.Info(context.Background(), "shutdown signal received")

	// fail health checks to drain connections
	shutdownGate.Set("draining")
	L.Info(context.Background(), "shutdown gate closed")

	// Wait for in-flight requests to finish and for load balancer
	// to detect unhealthy and stop sending new requests.
	drainDuration := time.Duration(appCfg.DrainSeconds) * time.Second
	L.Info(context.Background(), "sleeping for drain period", "drain_seconds", appCfg.DrainSeconds)
	forceCh := make(chan os.Signal, 1)
	signal.Notify(forceCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-time.After(drainDuration):
		L.Info(context.Background(), "drain period complete")
	case <-forceCh:
		L.Warn(context.Background(), "second signal received, skipping drain")
	}
	signal.Stop(forceCh)

	// Shutdown components with per-component budget sliced from total.
	// stopProf is synchronous and needs no context, so it's excluded.
	type stopFn struct {
		name string
		fn   func(context.Context) error
	}
	stopFns := []stopFn{
		{"alertapi http server", alertapiHTTPStop},
		{"ops http server", opsHTTPStop},
		{"otel", shutdownOtelx},
	}

	budget := time.Duration(appCfg.ShutdownBudgetSeconds) * time.Second
	perComponent := budget / time.Duration(len(stopFns))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	for _, s := range stopFns {
		cctx, ccancel := context.WithTimeout(shutdownCtx, perComponent)
		if err := s.fn(cctx); err != nil {
			L.Error(context.Background(), err, s.name+" shutdown")
		}
		ccancel()
	}

	stopProf()

	L.Info(context.Background(), "shutdown complete")
	return nil
}

func notifySystemd() error {
	// systemd will set NOTIFY_SOCKET to a unix socket path if we were started under systemd with type=notify
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return fmt.Errorf("NOTIFY_SOCKET not set, skipping systemd notify")
	}
	conn, err := net.Dial("unixgram", addr) //nolint:gosec,noctx // G704: addr is from NOTIFY_SOCKET set by systemd not user input, no context support in net package for unixgram sockets
	if err != nil {
		return fmt.Errorf("systemd notify failed: dial failed: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("READY=1")); err != nil {
		return fmt.Errorf("systemd notify failed: write failed: %w", err)
	}
	return nil
}
