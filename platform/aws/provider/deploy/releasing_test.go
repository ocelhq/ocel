package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
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
	opened, err := stacksWith(cfg, engine).at(context.Background(), provider.StackRef{}, "")
	if err != nil {
		t.Fatalf("open a release: %v", err)
	}
	return opened
}
