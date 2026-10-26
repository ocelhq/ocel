package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func nextAppBuiltFromItsFunction(t *testing.T) provider.StackSpec {
	t.Helper()
	app := anApp()
	app.Framework = buildoutput.FrameworkNext
	app.Router = "switchboard"
	app.Functions = []provider.FunctionSpec{{Name: "server", Route: "server", Framework: buildoutput.Framework{Name: buildoutput.FrameworkNext}}}
	app.Routing = &provider.RoutingSpec{RootFunction: "server", RouteTable: router.RouteTable{Format: edge.RouteTableNext, Table: []byte(`{"rootFunction":"server"}`)}}
	app.ISR = &provider.ISRSpec{TagNamespace: "PROJECT#shop#STACK#prod--web--r0a1b2c3d#TAG#"}
	app.Static = &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}}
	spec := aStack(t, app)
	spec.App.StorageCoordinate = naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: spec.Ref.Name.Release}
	return spec
}

func TestANextContainerBuiltFromItsFunctionOnABoxNothingFrontsIsRefused(t *testing.T) {
	t.Parallel()

	machine := &box{}
	_, err := over(machine).ProvisionContainers(context.Background(), nextAppBuiltFromItsFunction(t), progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "rebuild it") {
		t.Fatalf("ProvisionContainers() = %v, want a refusal to rebuild it: on a box nothing dispatches, next start serves a Next app", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, "docker run") {
			t.Errorf("the box ran %q, want nothing started for a container no worker serves", command)
		}
	}
}

func TestANextContainerCloudflareFrontsIsToldTheWorkerKeepsWhatItTagsAndWhereARevalidationPurgesIt(t *testing.T) {
	t.Parallel()

	spec := nextAppBuiltFromItsFunction(t)
	spec.Edge = cloudflare.New("ocel", cloudflare.Origin{}, cloudflare.Options{Cache: true})
	spec.App.ISR.PurgeURL = "https://shop.acct.workers.dev/purge"

	env := vps.NewFrontedEnv(spec, "s3cret")

	for name, want := range map[string]string{
		edge.EdgeCacheVar:         string(edge.EdgeCacheMarked),
		edge.CacheTagPurgeVar:     "1",
		edge.ReleaseTokenVar:      spec.Ref.Name.Release.String(),
		edge.PurgeURLVar:          "https://shop.acct.workers.dev/purge",
		edge.PurgeSecretVar:       "s3cret",
		"OCEL_STORAGE_COORDINATE": "shop/prod/web/r0a1b2c3d",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q: the container tags what the worker keeps and purges it on revalidation", name, env[name], want)
		}
	}
}

func TestANextContainerCloudflareFrontsAnswersTheImagesTheWorkerPostsToIt(t *testing.T) {
	t.Parallel()

	spec := nextAppBuiltFromItsFunction(t)
	spec.Edge = cloudflare.New("ocel", cloudflare.Origin{}, cloudflare.Options{Cache: true})

	if env := vps.NewFrontedEnv(spec, ""); env[edge.ImageEndpointVar] != "1" {
		t.Errorf("%s = %q, want 1: the worker posts /_next/image to the container's image path", edge.ImageEndpointVar, env[edge.ImageEndpointVar])
	}
}
