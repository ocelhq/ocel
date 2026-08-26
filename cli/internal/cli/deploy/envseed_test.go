package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type envOptions struct {
	folder      string
	environment string
	preview     bool
}

type envRefOptions struct {
	project string
}

func gateTier(opts envOptions) environmentv1.Tier {
	if opts.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func envSet(t *testing.T, _ string, key, value string, opts envOptions) {
	t.Helper()
	seedCell(t, gateTier(opts), &envvarsv1.Coordinate{Slug: "test-app", Folder: opts.folder, Key: key, Environment: opts.environment}, clitest.FakeCellData{Value: value})
}

func envRef(t *testing.T, _ string, key string, opts envOptions, ref envRefOptions) {
	t.Helper()
	seedCell(t, gateTier(opts), &envvarsv1.Coordinate{Slug: "test-app", Key: key}, clitest.FakeCellData{Target: &clitest.FakeCoordinate{Slug: ref.project, Key: key}})
}

func ownedElsewhere(t *testing.T, key, value string) {
	t.Helper()
	seedCell(t, environmentv1.Tier_TIER_PRODUCTION, &envvarsv1.Coordinate{Slug: "platform", Key: key}, clitest.FakeCellData{Value: value})
}

func seedCell(t *testing.T, tier environmentv1.Tier, c *envvarsv1.Coordinate, data clitest.FakeCellData) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	if err := store.Write(tier, c, data); err != nil {
		t.Fatalf("seed %s: %v", c.GetKey(), err)
	}
}
