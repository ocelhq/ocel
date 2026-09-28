package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestADeploymentRecordMarshalsUnderItsWireNamesAndOmitsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	t.Run("audit fields are omitted when absent", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, DeploymentRecord{App: "web", Build: "b1"})
		for _, absent := range []string{"valueFingerprint", "variables"} {
			if strings.Contains(raw, absent) {
				t.Errorf("record = %s, want no %q for a Deployment that baked nothing", raw, absent)
			}
		}
	})

	t.Run("audit fields marshal under their wire names", func(t *testing.T) {
		t.Parallel()

		raw := marshalRecord(t, DeploymentRecord{
			App:              "web",
			Build:            "b1~fp",
			ValueFingerprint: "fp",
			Variables: []VariableRecord{
				{Key: "PLAIN_KEY", Version: 2},
				{Key: "LIVE_KEY", Folder: "/api", Live: true},
			},
		})

		var got struct {
			ValueFingerprint string           `json:"valueFingerprint"`
			Variables        []VariableRecord `json:"variables"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.ValueFingerprint != "fp" {
			t.Errorf("valueFingerprint = %q, want %q", got.ValueFingerprint, "fp")
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

		raw := marshalRecord(t, DeploymentRecord{
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

		raw := marshalRecord(t, DeploymentRecord{Needs: []edge.Need{}, SupportInEffect: []edge.Need{}, Waived: []edge.Need{}})
		for _, absent := range []string{`"needs"`, `"supportInEffect"`, `"waived"`} {
			if strings.Contains(raw, absent) {
				t.Errorf("record = %s, want no %s for a Deployment that declared no need", raw, absent)
			}
		}
	})
}

func marshalRecord(t *testing.T, record DeploymentRecord) string {
	t.Helper()

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(raw)
}
