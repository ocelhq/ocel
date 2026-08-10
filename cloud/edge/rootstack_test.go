package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

// A worker-name stem names a family: the stem itself and every name segmented
// below it. It is deliberately not a raw string prefix — a sibling whose name
// merely starts with the same characters is a different family, and pruning and
// teardown both act on what this answers.
func TestNameUnderStem(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stem, script string
		want         bool
	}{
		{"the stem itself", "ocel-shop-preview", "ocel-shop-preview", true},
		{"a name segmented below it", "ocel-shop-preview", "ocel-shop-preview-web", true},
		{"two segments below it", "ocel-shop-preview", "ocel-shop-preview-web-api", true},
		{"a sibling sharing only characters", "ocel-shop-preview", "ocel-shop-previewer", false},
		{"another project's stem", "ocel-shop-preview", "ocel-other-preview", false},
		{"a sibling project slugged past ours", "ocel-shop-preview", "ocel-shopfoo-preview", false},
		{"a production worker of the same project", "ocel-shop-preview", "ocel-shop-prod-web", false},
		{"an empty stem matches nothing", "", "ocel-shop-preview", false},
		{"an empty name is nothing's", "ocel-shop-preview", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NameUnderStem(tc.stem, tc.script); got != tc.want {
				t.Errorf("NameUnderStem(%q, %q) = %v, want %v", tc.stem, tc.script, got, tc.want)
			}
		})
	}
}

// The audit fields are additions to a record shape already on the wire, so a
// Deployment that bakes nothing must still marshal byte-for-byte as it did
// before them: a record the store already holds and one written now differ
// only where there is something to say.
func TestDeploymentRecord_AuditFieldsAreOmittedWhenAbsent(t *testing.T) {
	raw, err := json.Marshal(DeploymentRecord{App: "web", Identity: "b1"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, absent := range []string{"valueFingerprint", "variables"} {
		if strings.Contains(string(raw), absent) {
			t.Errorf("record = %s, want no %q for a Deployment that baked nothing", raw, absent)
		}
	}
}

func TestDeploymentRecord_AuditFieldsMarshalUnderTheirWireNames(t *testing.T) {
	raw, err := json.Marshal(DeploymentRecord{
		App:              "web",
		Identity:         "b1~fp",
		ValueFingerprint: "fp",
		Variables: []VariableRecord{
			{Key: "PLAIN_KEY", Version: 2},
			{Key: "LIVE_KEY", Folder: "/api", Live: true},
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		ValueFingerprint string           `json:"valueFingerprint"`
		Variables        []VariableRecord `json:"variables"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
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
}
