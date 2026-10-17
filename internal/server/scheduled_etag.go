package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"emby-go/internal/store"
)

// Content validator, not a monotonic revision: run results and timestamps do
// not invalidate editing. Equal-content ABA remains valid by design.
func scheduledConfigETag(task store.ScheduledTask) string {
	payload := struct {
		ID                                  int64
		CreatedAt, Name, Type, Cron, Params string
		Enabled                             bool
	}{task.ID, task.CreatedAt, task.Name, task.Type, task.Cron, task.Params, task.Enabled}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return `"sc1-` + hex.EncodeToString(sum[:]) + `"`
}

func scheduledResponse(task store.ScheduledTask) scheduledView {
	return scheduledView{ScheduledTask: task, ConfigETag: scheduledConfigETag(task)}
}
