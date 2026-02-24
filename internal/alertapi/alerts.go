package alertapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/triage"
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
		// skip resolved alerts for now
		if al.Status != "firing" {
			continue
		}

		// dedup: skip if we've already triaged this fingerprint
		if existing, ok := a.store.GetByFingerprint(al.Fingerprint); ok {
			if existing.Status == triage.StatusPending || existing.Status == triage.StatusInProgress {
				continue
			}
		}

		id := ulid.Make().String()
		result := &triage.Result{
			ID:          id,
			Fingerprint: al.Fingerprint,
			Status:      triage.StatusPending,
			Alert:       al.Labels["alertname"],
			Severity:    al.Labels["severity"],
			Summary:     al.Annotations["summary"],
			CreatedAt:   time.Now(),
		}

		a.store.Put(result)
		accepted = append(accepted, id)

		// kick off async triage
		go a.triage(r.Context(), result, &al)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	// nothing to do with errors here
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": accepted,
	})
}
