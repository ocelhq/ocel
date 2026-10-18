package cloudflare

import (
	"encoding/json"
	"testing"
)

func TestEveryEntryWorkerIsToldThePurgeBudgetTheEdgeStates(t *testing.T) {
	for name, built := range everyEntry(t) {
		t.Run(name, func(t *testing.T) {
			var told struct {
				PurgesPerMinute int `json:"purgesPerMinute"`
				TagsPerPurge    int `json:"tagsPerPurge"`
			}
			if err := json.Unmarshal([]byte(built.worker.Variables[envPurgeBudget]), &told); err != nil {
				t.Fatalf("Variables[%s] = %q, want the budget as JSON: %v", envPurgeBudget, built.worker.Variables[envPurgeBudget], err)
			}
			stated := New(defaultNamespace, Options{}).Facts().PurgeBudget
			if told.PurgesPerMinute != stated.PurgesPerMinute || told.TagsPerPurge != stated.TagsPerPurge {
				t.Errorf("the worker is told %+v, want the budget the edge's facts state, %+v", told, stated)
			}
		})
	}
}
