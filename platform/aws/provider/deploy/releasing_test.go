package deploy

import (
	"context"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func fixed(cfg Config) ReleaseConfig {
	return func(context.Context, Scope) (Config, error) { return cfg, nil }
}

func releasing(t *testing.T, cfg Config) *release {
	t.Helper()
	return releasingOn(t, cfg, nil)
}

func releasingOn(t *testing.T, cfg Config, engine *mockedEngine) *release {
	t.Helper()
	held, err := standingUp(cfg, engine).at(context.Background(), provider.StackRef{}, "")
	if err != nil {
		t.Fatalf("open a release: %v", err)
	}
	return held
}

func plannedValues(app *contractv1.ManifestApp) provider.AppValues {
	values := provider.AppValues{Plain: map[string]string{}, Folder: app.GetFolder()}
	for _, v := range app.GetVariables() {
		if v.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			values.Plain[v.GetKey()] = v.GetValue()
		}
	}
	return values
}

func plannedEnv(t *testing.T, cfg Config, app *contractv1.ManifestApp, front edge.Edge) map[string]string {
	t.Helper()
	spec := provider.StackSpec{
		Kind: provider.StackApp,
		Edge: front,
		App:  &provider.AppSpec{App: app.GetName(), Values: plannedValues(app)},
	}
	return releasing(t, cfg).appEnv(spec, appBundle{}, sessionScope{})
}
