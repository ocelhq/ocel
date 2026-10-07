package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestAReleaseRecordMarshalsUnderItsWireNamesAndOmitsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	t.Run("audit fields are omitted when absent", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, ReleaseRecord{App: "web", Release: "b1"})
		for _, absent := range []string{"releaseFingerprint", "variables"} {
			if strings.Contains(raw, absent) {
				t.Errorf("record = %s, want no %q for a release that baked nothing", raw, absent)
			}
		}
	})

	t.Run("audit fields marshal under their wire names", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, ReleaseRecord{
			App:                "web",
			Release:            "b1~fp",
			ReleaseFingerprint: "fp",
			Variables: []VariableRecord{
				{Key: "PLAIN_KEY", Version: 2},
				{Key: "LIVE_KEY", Folder: "/api", Live: true},
			},
		})

		var got struct {
			ReleaseFingerprint string           `json:"releaseFingerprint"`
			Variables          []VariableRecord `json:"variables"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.ReleaseFingerprint != "fp" {
			t.Errorf("releaseFingerprint = %q, want %q", got.ReleaseFingerprint, "fp")
		}
		if len(got.Variables) != 2 {
			t.Fatalf("variables = %v, want both entries", got.Variables)
		}
		if got.Variables[0] != (VariableRecord{Key: "PLAIN_KEY", Version: 2}) {
			t.Errorf("variables[0] = %+v, want the version it shipped at", got.Variables[0])
		}
		if got.Variables[1] != (VariableRecord{Key: "LIVE_KEY", Folder: "/api", Live: true}) {
			t.Errorf("variables[1] = %+v, want a latest-at-runtime entry with no version", got.Variables[1])
		}
	})

	t.Run("need fields marshal as need names under their wire names", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, ReleaseRecord{
			Needs:           []edge.Need{edge.NeedStreaming, edge.NeedEdgeCache},
			SupportInEffect: []edge.Need{edge.NeedStreaming},
			Waived:          []edge.Need{edge.NeedEdgeCache},
		})
		for _, want := range []string{
			`"needs":["streaming","edge-cache"]`,
			`"supportInEffect":["streaming"]`,
			`"waived":["edge-cache"]`,
		} {
			if !strings.Contains(raw, want) {
				t.Errorf("record = %s, want %s", raw, want)
			}
		}
	})

	t.Run("need fields are omitted when empty", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, ReleaseRecord{Needs: []edge.Need{}, SupportInEffect: []edge.Need{}, Waived: []edge.Need{}})
		for _, absent := range []string{`"needs"`, `"supportInEffect"`, `"waived"`} {
			if strings.Contains(raw, absent) {
				t.Errorf("record = %s, want no %s for a release that declared no need", raw, absent)
			}
		}
	})
}

func marshalRecord(t *testing.T, record ReleaseRecord) string {
	t.Helper()

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(raw)
}

func TestAReleaseRecordNamesItsReleaseAndItsBuildOnTheWire(t *testing.T) {
	t.Parallel()

	raw := marshalRecord(t, ReleaseRecord{App: "web", Release: "b1~fp", BuildID: "b1"})
	for _, want := range []string{`"release":"b1~fp"`, `"buildId":"b1"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("record = %s, want %s", raw, want)
		}
	}
	for _, absent := range []string{`"identity"`, `"deploymentId"`} {
		if strings.Contains(raw, absent) {
			t.Errorf("record = %s, want no %s: a release record is not a deployment", raw, absent)
		}
	}
}

func TestAPromotionNamesTheReleasesItMadeOnTheWire(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1~fp"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"releases":{"web":"b1~fp"}`; !strings.Contains(string(raw), want) {
		t.Errorf("promotion = %s, want %s", raw, want)
	}
	if strings.Contains(string(raw), `"builds"`) {
		t.Errorf("promotion = %s, want no builds: a promotion maps apps to releases", raw)
	}
}

func TestAPruneNamesTheReleasesItRemovedOnTheWire(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(PruneResult{DeploymentRemovals: []PointerRemoval{{Pointer: "main@p1"}}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `"deploymentRemovals":[{"pointer":"main@p1"}]`; !strings.Contains(string(raw), want) {
		t.Errorf("prune = %s, want %s", raw, want)
	}
}
