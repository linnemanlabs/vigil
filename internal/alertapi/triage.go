package alertapi

import (
	"context"

	"github.com/linnemanlabs/vigil/internal/alert"
	"github.com/linnemanlabs/vigil/internal/triage"
)

func (a *API) triage(ctx context.Context, result *triage.Result, al *alert.Alert) {
	result.Status = triage.StatusInProgress
	a.store.Put(result)

	// todo: triage orchestration

	result.Status = triage.StatusComplete
	result.Analysis = "placeholder - triage engine not yet implemented"
	a.store.Put(result)
}
