package cloudflare

import (
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/edge"
)

const envPurgeBudget = "OCEL_PURGE_BUDGET"

var workersCachePurgeBudget = edge.PurgeBudget{TagsPerPurge: 100}

func withPurgeBudget(worker workerScript, budget edge.PurgeBudget) (workerScript, error) {
	encoded, err := json.Marshal(struct {
		TagsPerPurge int `json:"tagsPerPurge"`
	}{budget.TagsPerPurge})
	if err != nil {
		return workerScript{}, err
	}
	return withVar(worker, envPurgeBudget, string(encoded)), nil
}
