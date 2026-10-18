package cloudflare

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/edge"
)

const envPurgeBudget = "OCEL_PURGE_BUDGET"

var workersCachePurgeBudget = edge.PurgeBudget{PurgesPerMinute: 5, TagsPerPurge: 100}

func withPurgeBudget(worker workerScript, budget edge.PurgeBudget) (workerScript, error) {
	encoded, err := json.Marshal(struct {
		PurgesPerMinute int `json:"purgesPerMinute"`
		TagsPerPurge    int `json:"tagsPerPurge"`
	}{budget.PurgesPerMinute, budget.TagsPerPurge})
	if err != nil {
		return workerScript{}, err
	}
	return withVar(worker, envPurgeBudget, string(encoded)), nil
}
