package alertapi

import (
	"context"
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/go-core/xerrors"
	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/triage"
)

// TriageService defines the business operations alertapi needs.
type TriageService interface {
	Submit(ctx context.Context, al *alert.Alert) (*triage.SubmitResult, error)
	Get(ctx context.Context, id string) (*triage.Result, bool, error)
}

// API holds dependencies for HTTP handlers.
type API struct {
	logger log.Logger
	svc    TriageService
}

// New creates a new API handler.
func New(logger log.Logger, svc TriageService) *API {
	if logger == nil {
		logger = log.Nop()
	}
	if svc == nil {
		panic(xerrors.New("triage service is required"))
	}
	return &API{
		logger: logger,
		svc:    svc,
	}
}

// RegisterRoutes attaches API endpoints to the router.
func (a *API) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/alerts", a.handleIngestAlert)
		r.Get("/triage/{id}", a.handleGetTriage)
	})
}

func (a *API) handleGetTriage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.String("vigil.triage.id", id))

	result, ok, err := a.svc.Get(r.Context(), id)
	if err != nil {
		a.logger.Error(r.Context(), err, "failed to get triage result", "id", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	span.SetAttributes(attribute.String("vigil.triage.status", string(result.Status)))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
