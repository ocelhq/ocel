package surface_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

type disk struct {
	Name   string
	SizeGB int64
	Type   string
	Rules  []rule
	Labels map[string]string
}

type rule struct {
	Port  int64
	Proto string
}

type diskUpdate struct {
	Mask   []string
	Disk   disk
	Set    map[string]string
	Remove []string
}

type pulumiRule struct {
	Port     int64  `json:"port"`
	Protocol string `json:"protocol"`
}

var diskSurface = surface.Surface[disk, diskUpdate]{
	Name: "disk",
	Fields: []surface.Field[disk, diskUpdate]{
		{
			Name: "size",
			Paths: []surface.Path[disk]{surface.NewPath("size", func(d *disk, gb int64) error {
				d.SizeGB = gb
				return nil
			})},
			Same: func(current, desired *disk) bool { return current.SizeGB == desired.SizeGB },
			Update: func(u *diskUpdate, _, desired *disk) {
				u.Mask = append(u.Mask, "sizeGb")
				u.Disk.SizeGB = desired.SizeGB
			},
			Reset: func(desired *disk) {
				if desired.SizeGB == 0 {
					desired.SizeGB = 10
				}
			},
			Lowers: func(current, desired *disk) bool { return desired.SizeGB < current.SizeGB },
		},
		{
			Name: "type",
			Paths: []surface.Path[disk]{surface.NewPath("type", func(d *disk, kind string) error {
				d.Type = kind
				return nil
			})},
			Same:   func(current, desired *disk) bool { return current.Type == desired.Type },
			Update: func(u *diskUpdate, _, desired *disk) { u.Mask = append(u.Mask, "type"); u.Disk.Type = desired.Type },
		},
		{
			Name: "firewall rules",
			Paths: []surface.Path[disk]{surface.NewPath("rules", func(d *disk, rules []pulumiRule) error {
				for _, r := range rules {
					d.Rules = append(d.Rules, rule{Port: r.Port, Proto: r.Protocol})
				}
				return nil
			})},
		},
	},
	Labels: &surface.Labels[disk, diskUpdate]{
		Of: func(d *disk) *map[string]string { return &d.Labels },
		Update: func(u *diskUpdate, set map[string]string, removed []string) {
			u.Set, u.Remove = set, removed
		},
	},
}

func ocelDisk() disk {
	return disk{Name: "data", Type: "pd-standard", Rules: []rule{{Port: 22, Proto: "tcp"}}, Labels: map[string]string{"ocel-tier": "production"}}
}

func TestASurfacePatchesEveryLeafItsTranslatorsDecode(t *testing.T) {
	got := diskSurface.Paths()
	want := []string{"rules.port", "rules.protocol", "size", "type"}
	if !slices.Equal(got, want) {
		t.Errorf("Paths() = %v, want %v: a path is patchable exactly where a translator decodes it", got, want)
	}
}

func TestAPatchIsMergedOntoOcelsDesiredObject(t *testing.T) {
	patched, err := diskSurface.Patch("disk data", ocelDisk(), map[string]any{
		"size":  float64(50),
		"rules": []any{map[string]any{"port": float64(443), "protocol": "tcp"}},
	}, nil, surface.Recorded{})
	if err != nil {
		t.Fatalf("Patch() = %v", err)
	}
	if patched.Desired.SizeGB != 50 {
		t.Errorf("desired size %d, want the 50 the patch set", patched.Desired.SizeGB)
	}
	if patched.Desired.Type != "pd-standard" {
		t.Errorf("desired type %q, want ocel's pd-standard left alone where nothing patched it", patched.Desired.Type)
	}
	if !slices.Equal(patched.Desired.Rules, []rule{{22, "tcp"}, {443, "tcp"}}) {
		t.Errorf("desired rules %v, want the patch's rule after ocel's own", patched.Desired.Rules)
	}
	if !patched.Sets("size") || patched.Sets("type") {
		t.Errorf("Sets(size) = %v, Sets(type) = %v, want only what the patch set read as set", patched.Sets("size"), patched.Sets("type"))
	}
}

func TestAPatchToAPathNoTranslatorDecodesIsRefusedNamingIt(t *testing.T) {
	for name, patch := range map[string]map[string]any{
		"a field ocel owns":       {"name": "mine"},
		"an unknown nested field": {"rules": []any{map[string]any{"port": float64(1), "priority": float64(2)}}},
		"an object for a leaf":    {"size": map[string]any{"gb": float64(3)}},
		"an empty list":           {"zones": []any{}},
		"an empty object":         {"encryption": map[string]any{}},
		"a null":                  {"encryption": nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := diskSurface.Patch("disk data", ocelDisk(), patch, nil, surface.Recorded{})
			if err == nil {
				t.Fatal("Patch() = nil, want the path refused")
			}
			for key := range patch {
				if !strings.Contains(err.Error(), "disk."+key) {
					t.Errorf("Patch() = %v, want it to name disk.%s", err, key)
				}
			}
		})
	}
}

func TestAnEmptyValueOverPathsATranslatorDecodesIsNotRefused(t *testing.T) {
	for name, patch := range map[string]map[string]any{
		"an empty list":             {"rules": []any{}},
		"a list of an empty object": {"rules": []any{map[string]any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := diskSurface.Patch("disk data", ocelDisk(), patch, nil, surface.Recorded{}); err != nil {
				t.Errorf("Patch() = %v, want nil: the value sits over rules.port and rules.protocol, which a translator decodes", err)
			}
		})
	}
}

func TestAPatchedFieldIsPartOfTheDiffAndItsUpdate(t *testing.T) {
	current := ocelDisk()
	current.SizeGB = 10
	current.Type = "pd-ssd"
	patched, err := diskSurface.Patch("disk data", ocelDisk(), map[string]any{"size": float64(50)}, nil, surface.Recorded{})
	if err != nil {
		t.Fatal(err)
	}

	var update diskUpdate
	differences, err := patched.Drift(&current, &update)
	if err != nil {
		t.Fatalf("Drift() = %v", err)
	}
	if len(differences) != 1 || !strings.Contains(differences[0], "size") {
		t.Errorf("Drift() = %v, want the patched size alone: a field nothing patched or recorded stays ocel's own diff", differences)
	}
	if !slices.Equal(update.Mask, []string{"sizeGb"}) || update.Disk.SizeGB != 50 {
		t.Errorf("update %+v, want the size sent at 50", update)
	}
}

func TestARecordedPathNoLongerPatchedGoesBackToOcelsValue(t *testing.T) {
	current := ocelDisk()
	current.Type = "pd-ssd"
	patched, err := diskSurface.Patch("disk data", ocelDisk(), nil, nil, surface.Recorded{Paths: []string{"type"}})
	if err != nil {
		t.Fatal(err)
	}

	var update diskUpdate
	differences, err := patched.Drift(&current, &update)
	if err != nil {
		t.Fatalf("Drift() = %v", err)
	}
	if len(differences) != 1 || update.Disk.Type != "pd-standard" {
		t.Errorf("Drift() = %v with update %+v, want the type sent back to ocel's pd-standard", differences, update)
	}
	if len(patched.Recorded().Paths) != 0 {
		t.Errorf("Recorded() = %+v, want nothing left to record once the patch is gone", patched.Recorded())
	}
}

func TestARecordedPathOcelSetsNothingForIsClearedToGooglesDefault(t *testing.T) {
	patched, err := diskSurface.Patch("disk data", ocelDisk(), nil, nil, surface.Recorded{Paths: []string{"size"}})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Desired.SizeGB != 10 {
		t.Errorf("desired size %d, want Google's default of 10 once the patch that set it is gone", patched.Desired.SizeGB)
	}
}

func TestLoweringAOneWayFieldIsRefusedNamingIt(t *testing.T) {
	current := ocelDisk()
	current.SizeGB = 50
	for name, patch := range map[string]map[string]any{
		"a lower value":     {"size": float64(20)},
		"the patch removed": nil,
	} {
		t.Run(name, func(t *testing.T) {
			patched, err := diskSurface.Patch("disk data", ocelDisk(), patch, nil, surface.Recorded{Paths: []string{"size"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = patched.Drift(&current, &diskUpdate{})
			if err == nil || !strings.Contains(err.Error(), "disk data's size") {
				t.Errorf("Drift() = %v, want lowering the size refused by name", err)
			}
		})
	}
}

func TestRuleTagsBecomeLabelsAndARemovedTagIsRemoved(t *testing.T) {
	current := ocelDisk()
	current.Labels = map[string]string{"ocel-tier": "production", "team": "web", "cost-center": "old"}
	patched, err := diskSurface.Patch("disk data", ocelDisk(), nil, map[string]string{"cost-center": "platform"}, surface.Recorded{Labels: []string{"cost-center", "team"}})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Desired.Labels["cost-center"] != "platform" || patched.Desired.Labels["ocel-tier"] != "production" {
		t.Errorf("desired labels %v, want the tag beside ocel's own", patched.Desired.Labels)
	}

	var update diskUpdate
	if _, err := patched.Drift(&current, &update); err != nil {
		t.Fatal(err)
	}
	if update.Set["cost-center"] != "platform" || !slices.Equal(update.Remove, []string{"team"}) {
		t.Errorf("update set %v and removed %v, want cost-center set and the dropped team tag removed", update.Set, update.Remove)
	}
	if !slices.Equal(patched.Recorded().Labels, []string{"cost-center"}) {
		t.Errorf("Recorded() = %+v, want the one tag applied", patched.Recorded())
	}
}

func TestATagGoogleCloudCannotHoldAsALabelIsRefused(t *testing.T) {
	for name, tags := range map[string]map[string]string{
		"ocel's own prefix":  {"ocel-app": "web"},
		"the tier tag key":   {"ocel-tier": "production"},
		"an uppercase key":   {"Team": "web"},
		"a colon":            {"acme:team": "web"},
		"an uppercase value": {"team": "Web"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := diskSurface.Patch("disk data", ocelDisk(), nil, tags, surface.Recorded{})
			if err == nil {
				t.Fatalf("Patch() = nil, want %v refused", tags)
			}
			for key := range tags {
				if !strings.Contains(err.Error(), key) {
					t.Errorf("Patch() = %v, want it to name %s", err, key)
				}
			}
		})
	}
}
