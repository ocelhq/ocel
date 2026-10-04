package aws_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var writingActions = map[string]bool{
	"ACTION_CREATE": true, "ACTION_UPDATE": true, "ACTION_REPLACE": true, "ACTION_DELETE": true, "ACTION_DISABLE_THEN_DELETE": true,
}

type plannedEvent struct {
	Operation struct {
		Phase string `json:"phase"`
		Plan  *struct {
			Groups []struct {
				Name    string `json:"name"`
				Action  string `json:"action"`
				Changes []struct {
					Name   string `json:"name"`
					Action string `json:"action"`
				} `json:"changes"`
			} `json:"groups"`
		} `json:"plan"`
	} `json:"operation"`
}

func plannedWrites(t *testing.T, stream string) []string {
	t.Helper()
	planned := false
	var writes []string
	for line := range strings.Lines(stream) {
		var ev plannedEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		planned = planned || ev.Operation.Phase == "PHASE_PLAN"
		if ev.Operation.Plan == nil {
			continue
		}
		for _, group := range ev.Operation.Plan.Groups {
			acting := false
			for _, change := range group.Changes {
				if change.Action != "ACTION_KEEP" {
					acting = true
				}
				if writingActions[change.Action] {
					writes = append(writes, group.Name+"/"+change.Name+" "+change.Action)
				}
			}
			if !acting && writingActions[group.Action] {
				writes = append(writes, group.Name+" "+group.Action)
			}
		}
	}
	if !planned {
		t.Fatalf("the run streamed no plan phase, so nothing here reads what it would write:\n%s", stream)
	}
	return writes
}

func TestAPlanStreamWritesOnlyWhereAGroupOrOneOfItsChangesWrites(t *testing.T) {
	kept := strings.Join([]string{
		`INFO  [plan] a line a human reads`,
		`{"operation":{"time":"2026-09-27T10:00:01Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","started":{}}}`,
		`{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP","changes":[{"name":"OcelDispatchFunction","action":"ACTION_KEEP"},{"name":"OcelOriginSecret","action":"ACTION_ADOPT"}]},{"kind":"edge","name":"cloudfront/edge","action":"ACTION_ADOPT"}]}}}`,
	}, "\n")
	if writes := plannedWrites(t, kept); len(writes) > 0 {
		t.Errorf("a plan that keeps and adopts reads as writing %v", writes)
	}

	mixed := `{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_UPDATE","changes":[{"name":"OcelDispatchFunction","action":"ACTION_UPDATE"},{"name":"OcelOriginSecret","action":"ACTION_KEEP"}]},{"kind":"edge","name":"cloudflare/edge","action":"ACTION_CREATE"}]}}}`
	want := []string{"aws/ocel-bootstrap/OcelDispatchFunction ACTION_UPDATE", "cloudflare/edge ACTION_CREATE"}
	if writes := plannedWrites(t, mixed); !slices.Equal(writes, want) {
		t.Errorf("a plan that updates one change and creates one group reads as writing %v, want %v", writes, want)
	}
}
