package image

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/secrets"

	"github.com/ocelhq/ocel/pkg/images"
)

var machineKey = []byte("a machine's key")

func hashValues(values map[string]string) string {
	return NewLiveValues(values, machineKey).Hash
}

func TestTheLiveHashChangesWhenAValueDoesAndWhenAKeyDoes(t *testing.T) {
	hash := hashValues(map[string]string{"A": "1", "B": "2"})

	if hashValues(map[string]string{"B": "2", "A": "1"}) != hash {
		t.Error("the hash depends on the order the values were listed in, so an unchanged build misses its cache")
	}
	if hashValues(map[string]string{"A": "1", "B": "3"}) == hash {
		t.Error("the hash is the same after a value changed, so a build reuses a layer built with the old value")
	}
	if hashValues(map[string]string{"A": "1", "C": "2"}) == hash {
		t.Error("the hash is the same after a key changed")
	}
	if hashValues(map[string]string{"A": "12", "B": ""}) == hash {
		t.Error("the hash cannot tell where one value ends and the next begins")
	}
}

func TestTheLiveHashOfTheSameValuesDiffersFromOneMachinesKeyToAnother(t *testing.T) {
	values := map[string]string{"OCEL_SECRET_PIN": "1234"}

	if NewLiveValues(values, []byte("one machine")).Hash == NewLiveValues(values, []byte("another machine")).Hash {
		t.Error("the hash is the same under two keys, so anyone who reads it can try every short value until one matches")
	}
}

func TestTheLiveHashHoldsNoValue(t *testing.T) {
	hash := hashValues(map[string]string{"A": "a-very-recognisable-value"})

	if strings.Contains(hash, "recognisable") {
		t.Errorf("the hash %q contains a value", hash)
	}
}

func TestNoLiveValuesHaveNoHash(t *testing.T) {
	if got := NewLiveValues(nil, machineKey); got.Hash != "" || got.hasValues() {
		t.Errorf("NewLiveValues(nil) = %+v, want none", got)
	}
}

func TestTheSessionAnswersEachSecretWithItsValueAndNoOtherID(t *testing.T) {
	attachables := NewLiveValues(map[string]string{"OCEL_RESOURCE_POSTGRES_my-db": "postgres://127.0.0.1:5000"}, machineKey).newSecretsSession()

	if len(attachables) != 1 {
		t.Fatalf("the session attaches %d providers, want the one holding the secrets", len(attachables))
	}
	server, ok := attachables[0].(secrets.SecretsServer)
	if !ok {
		t.Fatalf("the attachment %T does not serve secrets", attachables[0])
	}
	got, err := server.GetSecret(context.Background(), &secrets.GetSecretRequest{ID: "OCEL_RESOURCE_POSTGRES_my-db"})
	if err != nil || string(got.Data) != "postgres://127.0.0.1:5000" {
		t.Errorf("GetSecret(OCEL_RESOURCE_POSTGRES_my-db) = %q, %v, want its value", got.GetData(), err)
	}
	if _, err := server.GetSecret(context.Background(), &secrets.GetSecretRequest{ID: "HOME"}); err == nil {
		t.Error("the session answered a secret nobody gave it")
	}
}

func newDockerfileRecipe(t *testing.T) Recipe {
	return Recipe{App: App{Name: "web", Workspace: at(t, "testdata/dockerfileapp")}, Dockerfile: "testdata/dockerfileapp/Dockerfile"}
}

func newRailpackRecipe(t *testing.T) Recipe {
	return Recipe{App: App{Name: "web", Workspace: at(t, "testdata/plainserver")}}
}

func solveFor(t *testing.T, recipe Recipe, live LiveValues) client.SolveOpt {
	t.Helper()
	opt, done, err := recipe.solve("", live)
	if err != nil {
		t.Fatalf("solve() = %v", err)
	}
	t.Cleanup(done)
	return opt
}

func TestABuildWithNoLiveValuesAttachesNoSessionAndAsksForNoEntitlement(t *testing.T) {
	for name, recipe := range map[string]Recipe{"dockerfile": newDockerfileRecipe(t), "railpack": newRailpackRecipe(t)} {
		t.Run(name, func(t *testing.T) {
			opt := solveFor(t, recipe, LiveValues{})

			if len(opt.Session) != 0 || len(opt.AllowedEntitlements) != 0 {
				t.Errorf("a build with no live values attaches %d session providers and the entitlements %v, want none", len(opt.Session), opt.AllowedEntitlements)
			}
			for attr := range opt.FrontendAttrs {
				if strings.HasPrefix(attr, "build-arg:") {
					t.Errorf("a build with no live values passes the build arg %s", attr)
				}
			}
		})
	}
}

func TestABuildWithLiveValuesAttachesThemAndAsksForHostNetworkSoItsStepsReachTheForwards(t *testing.T) {
	live := NewLiveValues(map[string]string{"OCEL_BINDING_DB": "x"}, machineKey)
	for name, recipe := range map[string]Recipe{"dockerfile": newDockerfileRecipe(t), "railpack": newRailpackRecipe(t)} {
		t.Run(name, func(t *testing.T) {
			opt := solveFor(t, recipe, live)

			if len(opt.Session) != 1 {
				t.Errorf("the solve attaches %d session providers, want the one holding the secrets", len(opt.Session))
			}
			if len(opt.AllowedEntitlements) != 1 || opt.AllowedEntitlements[0] != "network.host" {
				t.Errorf("the solve asks for the entitlements %v, want network.host alone", opt.AllowedEntitlements)
			}
		})
	}
}

func TestEachBuilderIsHandedTheLiveHashAsTheBuildArgItBustsItsCacheWith(t *testing.T) {
	live := NewLiveValues(map[string]string{"OCEL_BINDING_DB": "x"}, machineKey)
	for recipe, arg := range map[string]string{"railpack": "build-arg:secrets-hash", "dockerfile": "build-arg:OCEL_LIVE_HASH"} {
		t.Run(recipe, func(t *testing.T) {
			chosen := map[string]Recipe{"dockerfile": newDockerfileRecipe(t), "railpack": newRailpackRecipe(t)}[recipe]

			opt := solveFor(t, chosen, live)

			if got := opt.FrontendAttrs[arg]; got != live.Hash {
				t.Errorf("the solve hands %s = %q, want the live hash %q, so a build run with a changed value reuses the layer built with the old one", arg, got, live.Hash)
			}
			if got := opt.FrontendAttrs[platformAttr]; got != "" {
				t.Errorf("a build for no stated architecture is pinned to %q", got)
			}
		})
	}
}

func TestTheLiveHashKeepsThePlatformABuildIsPinnedTo(t *testing.T) {
	opt, done, err := newRailpackRecipe(t).solve("arm64", NewLiveValues(map[string]string{"K": "v"}, machineKey))
	if err != nil {
		t.Fatalf("solve() = %v", err)
	}
	t.Cleanup(done)

	if got := opt.FrontendAttrs[platformAttr]; got != images.ContainerPlatform("arm64") {
		t.Errorf("the solve is pinned to %q, want %s", got, images.ContainerPlatform("arm64"))
	}
}

func TestAFailedBuildWithBindingsOnADaemonOnAnotherMachineSaysTheForwardsAreOutOfItsReach(t *testing.T) {
	live := NewLiveValues(map[string]string{"OCEL_BINDING_DB": "x"}, machineKey)
	failed := errors.New("connection refused")
	for address, elsewhere := range map[string]bool{
		"10.0.0.5:2375":   true,
		"build.host:2375": true,
		"127.0.0.1:2375":  false,
		"localhost:2375":  false,
		"[::1]:2375":      false,
	} {
		d := daemon{DockerHost: images.DockerHost{Address: "tcp://" + address, Network: "tcp", Target: address}}

		err := d.explainFailedBuild("web", live, failed)

		if says := strings.Contains(err.Error(), "another machine"); says != elsewhere {
			t.Errorf("a failed build on the daemon at %s says %q", address, err)
		}
		if !errors.Is(err, failed) {
			t.Errorf("the failure %v lost the error the daemon gave", err)
		}
	}
	local := daemon{DockerHost: images.DockerHost{Address: "unix:///var/run/docker.sock", Network: "unix", Target: "/var/run/docker.sock"}}
	if err := local.explainFailedBuild("web", live, failed); strings.Contains(err.Error(), "another machine") {
		t.Errorf("a failed build on the daemon on this machine says %q", err)
	}
	remote := daemon{DockerHost: images.DockerHost{Address: "tcp://10.0.0.5:2375", Network: "tcp", Target: "10.0.0.5:2375"}}
	if err := remote.explainFailedBuild("web", LiveValues{}, failed); strings.Contains(err.Error(), "another machine") {
		t.Errorf("a failed build with no bindings blames the forwards: %q", err)
	}
}
