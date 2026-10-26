package surface_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

type answering []transform.Result

func (a answering) Evaluate(context.Context, transform.Request) ([]transform.Result, error) {
	return a, nil
}

var diskAllowed = map[string]surface.Allowed{"disk": {"disk": diskSurface.Paths()}}

func diskRequest() transform.Request {
	return transform.Request{Provider: "gcp", EnvTier: "production", Env: "prod", Resources: []transform.Resource{{Type: "disk", Name: "data"}}}
}

func TestATransformIsRefusedBeforeAnythingIsProvisionedWhereItReachesNothingPatchable(t *testing.T) {
	for name, result := range map[string]transform.Result{
		"a surface the resource has none of": {Patches: transform.Patches{"snapshot": {"size": float64(1)}}},
		"a path no translator decodes":       {Patches: transform.Patches{"disk": {"name": "mine"}}},
		"a tag google refuses":               {Tags: map[string]string{"Team": "web"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := surface.Evaluate(context.Background(), answering{result}, diskRequest(), diskAllowed)
			if err == nil {
				t.Fatal("Evaluate() = nil, want the transform refused")
			}
			if !strings.Contains(err.Error(), "disk data") {
				t.Errorf("Evaluate() = %v, want it to name the resource the patch was written for", err)
			}
		})
	}
}

func TestWhatTheTransformsCameToReachesTheStackTheyWereEvaluatedFor(t *testing.T) {
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	evaluated, err := surface.Evaluate(context.Background(), answering{{Patches: transform.Patches{"disk": {"size": float64(50)}}}}, diskRequest(), diskAllowed)
	if err != nil {
		t.Fatal(err)
	}
	ctx := surface.WithEvaluated(context.Background(), ref, evaluated)

	read, found := surface.ReadEvaluated(ctx, ref)
	if !found || read.For("disk", "data").Patches["disk"]["size"] != float64(50) {
		t.Errorf("ReadEvaluated() = %+v, %v, want the patch evaluated for disk data", read.For("disk", "data"), found)
	}
	other := ref
	other.Name = naming.InfraStack("staging")
	if _, found := surface.ReadEvaluated(ctx, other); found {
		t.Error("ReadEvaluated() found transforms for a stack they were not evaluated for")
	}
}

func TestABindingOutputLeftUnresolvedIsSplitFromWhatIsKnown(t *testing.T) {
	known, unknown := diskSurface.SplitOutputs(map[string]any{
		"type": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "a", "property": "b"}},
		"size": float64(50),
	})
	if _, kept := known["type"]; kept || known["size"] != float64(50) {
		t.Errorf("known = %v, want the written-down size alone", known)
	}
	if !slices.Equal(unknown, []string{"type"}) {
		t.Errorf("unknown = %v, want the path a binding output fills", unknown)
	}
}

func TestAPathABindingOutputReachesAnyLeafOfIsSplitWhole(t *testing.T) {
	known, unknown := diskSurface.SplitOutputs(map[string]any{
		"rules": []any{map[string]any{
			"port":     map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "a", "property": "b"}},
			"protocol": "tcp",
		}},
		"size": float64(50),
	})
	if _, kept := known["rules"]; kept || known["size"] != float64(50) {
		t.Errorf("known = %v, want the written-down size alone", known)
	}
	if !slices.Equal(unknown, []string{"rules"}) {
		t.Errorf("unknown = %v, want the whole path a binding output sits in", unknown)
	}
}
