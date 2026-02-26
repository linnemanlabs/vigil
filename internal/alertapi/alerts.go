package alertapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/linnemanlabs/vigil/internal/alert"
)

func (a *API) handleIngestAlert(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.logger.Info(r.Context(), "raw webhook", "body", string(body))
	r.Body = io.NopCloser(bytes.NewReader(body))

	var wh alert.Webhook
	if err := json.NewDecoder(r.Body).Decode(&wh); err != nil {
		http.Error(w, `{"error":"invalid payload"}`, http.StatusBadRequest)
		return
	}

	var accepted []string

	for _, al := range wh.Alerts {
		sr, err := a.svc.Submit(r.Context(), &al)
		if err != nil {
			a.logger.Error(r.Context(), err, "submit failed", "fingerprint", al.Fingerprint)
			continue
		}
		if sr.Skipped {
			continue
		}
		accepted = append(accepted, sr.ID)
	}

	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(
		attribute.Int("vigil.alerts.count", len(wh.Alerts)),
		attribute.Int("vigil.alerts.accepted", len(accepted)),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": accepted,
	})
}
