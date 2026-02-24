package alertapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/linnemanlabs/go-core/log"
	"github.com/linnemanlabs/go-core/xerrors"
	"github.com/linnemanlabs/vigil/internal/triage"
)

// API holds dependencies for HTTP handlers.
type API struct {
	logger log.Logger
	store  *triage.Store
	engine *triage.Engine
	// triage service
	// alerts service
}

// New creates a new API handler.
// logger may be nil, in which case a no-op logger is used
func New(logger log.Logger, store *triage.Store, engine *triage.Engine) *API {
	if logger == nil {
		logger = log.Nop()
	}
	if store == nil {
		panic(xerrors.New("triage store is required"))
	}
	if engine == nil {
		panic(xerrors.New("triage engine is required"))
	}
	return &API{
		logger: logger,
		store:  store,
		engine: engine,
	}
}

// RegisterRoutes attaches API endpoints to the router
func (a *API) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/alerts", a.handleIngestAlert)
		r.Get("/triage/{id}", a.handleGetTriage)
	})
}

func (a *API) handleGetTriage(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
