// Package pgstore provides a PostgreSQL implementation of triage.Store.
package pgstore

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/linnemanlabs/vigil/internal/triage"
)

var tracer = otel.Tracer("github.com/linnemanlabs/vigil/internal/triage/pgstore")

//go:embed schema.sql
var schema string

// Store persists triage results in PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New connects to PostgreSQL, applies the schema, and returns a ready Store.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close shuts down the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

const triageColumns = `id, fingerprint, status, alert_name, severity, summary, analysis,
	actions, created_at, completed_at, duration_s, tokens_used, tool_calls, system_prompt, model`

// Get retrieves a triage result by ID.
//
//nolint:dupl // similar structure to GetByFingerprint is intentional
func (s *Store) Get(ctx context.Context, id string) (*triage.Result, bool, error) {
	ctx, span := tracer.Start(ctx, "pgstore.Get", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", "SELECT"),
	))
	defer span.End()

	query := `SELECT ` + triageColumns + ` FROM triage_runs WHERE id = $1`
	r, err := s.scanTriageRow(s.pool.QueryRow(ctx, query, id))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, false, err
	}
	if r == nil {
		return nil, false, nil
	}

	if err := s.loadConversation(ctx, r); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, false, err
	}

	return r, true, nil
}

// GetByFingerprint retrieves the most recent triage result for a fingerprint.
//
//nolint:dupl // similar structure to Get is intentional
func (s *Store) GetByFingerprint(ctx context.Context, fingerprint string) (*triage.Result, bool, error) {
	ctx, span := tracer.Start(ctx, "pgstore.GetByFingerprint", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", "SELECT"),
	))
	defer span.End()

	query := `SELECT ` + triageColumns + ` FROM triage_runs WHERE fingerprint = $1 ORDER BY created_at DESC LIMIT 1`
	r, err := s.scanTriageRow(s.pool.QueryRow(ctx, query, fingerprint))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, false, err
	}
	if r == nil {
		return nil, false, nil
	}

	if err := s.loadConversation(ctx, r); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, false, err
	}

	return r, true, nil
}

// Put inserts or updates a triage result (upsert on triage_runs only).
func (s *Store) Put(ctx context.Context, r *triage.Result) error {
	ctx, span := tracer.Start(ctx, "pgstore.Put", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", "UPSERT"),
	))
	defer span.End()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless

	if err := s.upsertTriage(ctx, tx, r); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// AppendTurn inserts a single message row and returns its database ID.
func (s *Store) AppendTurn(ctx context.Context, triageID string, seq int, turn *triage.Turn) (int, error) {
	ctx, span := tracer.Start(ctx, "pgstore.AppendTurn", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", "INSERT"),
	))
	defer span.End()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless

	msgID, err := s.insertMessage(ctx, tx, triageID, seq, turn)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, fmt.Errorf("commit: %w", err)
	}
	return msgID, nil
}

// AppendToolCalls inserts tool_call rows for an assistant turn, matched
// against the tool results from the following user turn.
func (s *Store) AppendToolCalls(ctx context.Context, triageID string, messageID, messageSeq int, turn *triage.Turn, toolResults map[string]*triage.ContentBlock) error {
	ctx, span := tracer.Start(ctx, "pgstore.AppendToolCalls", trace.WithAttributes(
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation.name", "INSERT"),
	))
	defer span.End()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless

	if err := s.insertToolCalls(ctx, tx, triageID, messageID, messageSeq, turn, toolResults); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) upsertTriage(ctx context.Context, tx pgx.Tx, r *triage.Result) error {
	actionsJSON, err := json.Marshal(r.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}

	var completedAt *time.Time
	if !r.CompletedAt.IsZero() {
		completedAt = &r.CompletedAt
	}

	query := `INSERT INTO triage_runs (
		id, fingerprint, status, alert_name, severity, summary, analysis,
		actions, created_at, completed_at, duration_s, tokens_used, tool_calls, system_prompt, model
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	ON CONFLICT (id) DO UPDATE SET
		fingerprint   = EXCLUDED.fingerprint,
		status        = EXCLUDED.status,
		alert_name    = EXCLUDED.alert_name,
		severity      = EXCLUDED.severity,
		summary       = EXCLUDED.summary,
		analysis      = EXCLUDED.analysis,
		actions       = EXCLUDED.actions,
		completed_at  = EXCLUDED.completed_at,
		duration_s    = EXCLUDED.duration_s,
		tokens_used   = EXCLUDED.tokens_used,
		tool_calls    = EXCLUDED.tool_calls,
		system_prompt = EXCLUDED.system_prompt,
		model         = EXCLUDED.model`

	_, err = tx.Exec(ctx, query,
		r.ID, r.Fingerprint, string(r.Status), r.Alert, r.Severity, r.Summary, r.Analysis,
		actionsJSON, r.CreatedAt, completedAt, r.Duration, r.TokensUsed, r.ToolCalls,
		r.SystemPrompt, r.Model,
	)
	if err != nil {
		return fmt.Errorf("upsert triage: %w", err)
	}
	return nil
}

func (s *Store) insertMessage(ctx context.Context, tx pgx.Tx, triageID string, seq int, turn *triage.Turn) (int, error) {
	contentJSON, err := json.Marshal(turn.Content)
	if err != nil {
		return 0, fmt.Errorf("marshal content seq %d: %w", seq, err)
	}

	var tokensIn, tokensOut *int
	if turn.Usage != nil {
		tokensIn = &turn.Usage.InputTokens
		tokensOut = &turn.Usage.OutputTokens
	}

	var messageID int
	err = tx.QueryRow(ctx,
		`INSERT INTO messages (triage_id, seq, role, content, tokens_in, tokens_out, created_at, duration_s, stop_reason, model)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id`,
		triageID, seq, turn.Role, contentJSON, tokensIn, tokensOut, turn.Timestamp,
		turn.Duration, turn.StopReason, turn.Model,
	).Scan(&messageID)
	if err != nil {
		return 0, fmt.Errorf("insert message seq %d: %w", seq, err)
	}
	return messageID, nil
}

func (s *Store) insertToolCalls(ctx context.Context, tx pgx.Tx, triageID string, messageID, seq int, turn *triage.Turn, toolResults map[string]*triage.ContentBlock) error {
	for i := range turn.Content {
		block := &turn.Content[i]
		if block.Type != "tool_use" {
			continue
		}

		inputBytes := len(block.Input)
		var output json.RawMessage
		var outputBytes int
		var isError bool

		if result, ok := toolResults[block.ID]; ok {
			output, _ = json.Marshal(result.Content)
			outputBytes = len(output)
			isError = result.IsError
		}

		_, err := tx.Exec(ctx,
			`INSERT INTO tool_calls (triage_id, message_id, message_seq, tool_name, input, output, input_bytes, output_bytes, is_error, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			triageID, messageID, seq, block.Name, block.Input, output, inputBytes, outputBytes, isError, turn.Timestamp,
		)
		if err != nil {
			return fmt.Errorf("insert tool_call %s seq %d: %w", block.Name, seq, err)
		}
	}
	return nil
}

// loadConversation reads messages and reconstructs the Conversation on a Result.
func (s *Store) loadConversation(ctx context.Context, r *triage.Result) error {
	rows, err := s.pool.Query(ctx,
		`SELECT seq, role, content, tokens_in, tokens_out, created_at, duration_s, stop_reason, model
		 FROM messages WHERE triage_id = $1 ORDER BY seq`,
		r.ID,
	)
	if err != nil {
		return fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var turns []triage.Turn
	for rows.Next() {
		var (
			seq         int
			role        string
			contentJSON []byte
			tokensIn    *int
			tokensOut   *int
			createdAt   time.Time
			durationS   float64
			stopReason  string
			model       string
		)
		if err := rows.Scan(&seq, &role, &contentJSON, &tokensIn, &tokensOut, &createdAt, &durationS, &stopReason, &model); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}

		var content []triage.ContentBlock
		if err := json.Unmarshal(contentJSON, &content); err != nil {
			return fmt.Errorf("unmarshal content seq %d: %w", seq, err)
		}

		turn := triage.Turn{
			Role:       role,
			Content:    content,
			Timestamp:  createdAt,
			StopReason: stopReason,
			Duration:   durationS,
			Model:      model,
		}
		if tokensIn != nil || tokensOut != nil {
			turn.Usage = &triage.Usage{}
			if tokensIn != nil {
				turn.Usage.InputTokens = *tokensIn
			}
			if tokensOut != nil {
				turn.Usage.OutputTokens = *tokensOut
			}
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate messages: %w", err)
	}

	if len(turns) > 0 {
		r.Conversation = &triage.Conversation{Turns: turns}
	}
	return nil
}

// scanTriageRow scans a single row into a triage.Result (without conversation).
// Returns (nil, nil) when no row is found.
func (s *Store) scanTriageRow(row pgx.Row) (*triage.Result, error) {
	var (
		r           triage.Result
		status      string
		actionsJSON []byte
		completedAt *time.Time
	)

	err := row.Scan(
		&r.ID, &r.Fingerprint, &status, &r.Alert, &r.Severity, &r.Summary, &r.Analysis,
		&actionsJSON, &r.CreatedAt, &completedAt, &r.Duration, &r.TokensUsed, &r.ToolCalls,
		&r.SystemPrompt, &r.Model,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan: %w", err)
	}

	r.Status = triage.Status(status)

	if completedAt != nil {
		r.CompletedAt = *completedAt
	}

	if err := json.Unmarshal(actionsJSON, &r.Actions); err != nil {
		return nil, fmt.Errorf("unmarshal actions: %w", err)
	}

	return &r, nil
}
