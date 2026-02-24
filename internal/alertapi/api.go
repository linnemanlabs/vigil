package alertapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
)

// API holds dependencies for HTTP handlers.
type API struct {
	logger log.Logger
	// triage service
	// alerts service
}

// New creates a new API handler.
// logger may be nil, in which case a no-op logger is used
func New(logger log.Logger) *API {
	if logger == nil {
		logger = log.Nop()
	}
	return &API{
		logger: logger,
	}
}

// RegisterRoutes attaches API endpoints to the router
func (a *API) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/alerts", a.handleIngestAlert)
		r.Get("/triage/{id}", a.handleGetTriage)
	})
}

func (a *API) handleIngestAlert(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleGetTriage(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
