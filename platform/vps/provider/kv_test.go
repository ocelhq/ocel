package vps_test

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	storeContainer = "shop-prod-web-r0a1b2c3d-cache-kv"
	recordedKV     = "9a8b7c6d5e4f30211203f4e5d6c7b8a9f0e1d2c3b4a59687"
)

func aKVStore(t *testing.T, version string) resources.ProvisionRequest {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return resources.ProvisionRequest{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Resource: provider.Resource{
			Name: "cache", Type: provider.BindingKV,
			KV: &provider.KVSpec{Version: version, Eviction: "allkeys-lru", MemoryBytes: 256 << 20},
		},
	}
}

func withRecordedKV(machine *box) {
	sealed := fakeSeal + base64.StdEncoding.EncodeToString([]byte(recordedKV))
	machine.kept = base64.StdEncoding.EncodeToString([]byte(sealed)) + "\n"
}

func TestAProviderOverABoxServesKVStores(t *testing.T) {
	t.Parallel()

	if served := over(&box{}).Facts().Bindings; !slices.Contains(served, provider.BindingKV) {
		t.Errorf("Facts().Bindings = %v, and a project declaring a kv store is refused at deploy on a box", served)
	}
}

func TestADeclaredKVStoreRunsAsOneConfinedContainerOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionKV(context.Background(), aKVStore(t, "9"), nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind a client to: %v", err)
	}
	for property, want := range map[string]string{
		provider.PropertyHost:     storeContainer,
		provider.PropertyPort:     "6379",
		provider.PropertyUsername: "ocel",
		provider.PropertyTLS:      "false",
	} {
		if got := binding.Properties[property]; got != want {
			t.Errorf("the binding's %s is %q, want %q", property, got, want)
		}
	}
	if binding.Name != "cache" || binding.Type != provider.BindingKV {
		t.Errorf("the binding is %s %q, want the kv store cache", binding.Type, binding.Name)
	}

	run := machine.at("'docker' 'run'")
	if run < 0 {
		t.Fatalf("nothing was started:\n%s", strings.Join(machine.commands(), "\n"))
	}
	runCommand := machine.commands()[run]
	for _, want := range []string{
		"'--network' 'ocel-production-shop'",
		"ocel.resource=cache",
		"'--cap-drop' 'ALL'",
		"'--user' 'valkey:valkey'",
		"dst=/data",
		"'valkey/valkey:9.",
		"@sha256:",
		"'valkey-server'",
		"'--maxmemory' '268435456'",
		"'--maxmemory-policy' 'allkeys-lru'",
		"'--appendonly' 'yes'",
		"'ocel.backup=vol'",
	} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the container was started without %q:\n%s", want, runCommand)
		}
	}
	for _, refused := range []string{"--publish", "'-p'", "--cap-add"} {
		if strings.Contains(runCommand, refused) {
			t.Errorf("the container was started with %s:\n%s", refused, runCommand)
		}
	}
	kept := machine.commands()[machine.at("'docker' 'volume' 'create'")]
	if !strings.Contains(kept, "'"+host.LabelGeneration+"=9'") {
		t.Errorf("the volume was created as %q, and the next deploy cannot tell which major wrote it", kept)
	}
}

func TestAKVPasswordReachesTheBoxOnlyOnStdinAndIsKeptSealed(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionKV(context.Background(), aKVStore(t, "9"), nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	password := binding.Properties[provider.PropertyPassword]
	if len(password) < 32 {
		t.Fatalf("the password is %d characters", len(password))
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, password) {
			t.Fatalf("the password rode a command line:\n%s", command)
		}
	}
	probed := slices.IndexFunc(machine.commands(), func(command string) bool {
		return strings.Contains(command, "--askpass") && strings.Contains(command, "'--interactive'")
	})
	if probed < 0 || !strings.Contains(machine.feeds()[probed], password) {
		t.Errorf("no deploy step proved the store takes the password, fed on stdin:\n%s", strings.Join(machine.commands(), "\n"))
	}
	for at, command := range machine.commands() {
		if strings.Contains(command, "/kept/") && strings.Contains(machine.feeds()[at], password) {
			t.Errorf("the box keeps the password in plaintext:\n%s", command)
		}
	}
	if !strings.Contains(strings.Join(machine.commands(), "\n"), "'--binding' 'cache'") {
		t.Error("the password is not sealed to the store it belongs to")
	}
}

func TestASecondDeployBindsToThePasswordTheRunningStoreWasHanded(t *testing.T) {
	t.Parallel()

	machine := &box{}
	withRecordedKV(machine)
	binding, err := over(machine).ProvisionKV(context.Background(), aKVStore(t, "9"), nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	if got := binding.Properties[provider.PropertyPassword]; got != recordedKV {
		t.Errorf("the binding names a password the running store was never handed")
	}
}

func TestAKVVersionNoImageIsPinnedForIsRefusedBeforeTheBoxIsReached(t *testing.T) {
	t.Parallel()

	machine := &box{}
	_, err := over(machine).ProvisionKV(context.Background(), aKVStore(t, "7"), nil)
	if err == nil || !strings.Contains(err.Error(), "cache") || !strings.Contains(err.Error(), `"7"`) {
		t.Fatalf("ProvisionKV() = %v, want version 7 refused naming the store", err)
	}
	if len(machine.commands()) != 0 {
		t.Errorf("the box was reached before the refusal: %v", machine.commands())
	}
}

func TestARemovedKVStoreTakesItsContainerDataPasswordAndBackups(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := over(machine)
	in := aKVStore(t, "9")
	binding, err := p.ProvisionKV(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := len(machine.commands())
	if err := p.RemoveResource(context.Background(), in.Ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	joined := strings.Join(machine.commands()[before:], "\n")
	for _, want := range []string{
		"docker rm --force '" + storeContainer + "'",
		"docker volume rm",
		host.KeptPath(environment.TierProduction, storeContainer),
		host.BackupsDir(environment.TierProduction, storeContainer),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("a teardown ran %q and never reached %s", joined, want)
		}
	}
}

func TestAKVStoreDeclaredUnderANewerMajorCopiesItsDataIntoTheNewGenerationWithAWayBack(t *testing.T) {
	t.Parallel()

	machine := &box{}
	withRecordedKV(machine)
	machine.refuses = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker volume ls") && strings.Contains(command, host.LabelGeneration):
			return session.Result{Stdout: "8\n"}, true
		case strings.Contains(command, ".State.Status"):
			return session.Result{Stdout: "exited valkey/valkey:8 0123456789ab\n"}, true
		}
		return session.Result{}, false
	}
	if _, err := over(machine).ProvisionKV(context.Background(), aKVStore(t, "9"), nil); err != nil {
		t.Fatalf("ProvisionKV() = %v, and a stopped store's files copy as well as a running one's", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "'dump'") {
		t.Errorf("a kv store was handed to the postgres dump:\n%s", joined)
	}
	swap := machine.commands()[machine.at("docker rename")]
	copied := strings.Index(swap, "'--entrypoint' 'cp'")
	for _, want := range []string{
		"trap ",
		"docker stop '" + storeContainer + "'",
		"src=" + storeContainer + "-g8,dst=/from,readonly",
		"src=" + storeContainer + "-g9,dst=/data",
		"'--network' 'none'",
		"docker volume rm '" + storeContainer + "-g8'",
	} {
		if !strings.Contains(swap, want) {
			t.Errorf("the swap never runs %s:\n%s", want, swap)
		}
	}
	if started := strings.Index(swap, "'valkey-server'"); copied < 0 || started < copied {
		t.Errorf("the new store starts before the old data is copied into its volume:\n%s", swap)
	}
	authenticated, retired := strings.Index(swap, "--askpass"), strings.Index(swap, "docker volume rm '"+storeContainer+"-g8'")
	if authenticated < 0 || retired < authenticated {
		t.Errorf("the swap retires the old data before the new store takes the password:\n%s", swap)
	}
	if fed := machine.feeds()[machine.at("docker rename")]; !strings.Contains(fed, recordedKV) {
		t.Errorf("the swap proves the new store takes the password without being fed it on stdin")
	}
}

func patchedKV(t *testing.T, pass *patching) (*box, error) {
	t.Helper()
	machine := &box{}
	p := over(machine)
	p.Transforming(pass)
	_, err := p.ProvisionKV(context.Background(), aKVStore(t, "9"), nil)
	return machine, err
}

func TestATransformTunesAKVStoreAfterTheSettingsOcelRenders(t *testing.T) {
	t.Parallel()

	pass := &patching{patches: transform.Patches{
		"container": {"args": []any{"--io-threads", "4"}, "memory": "1g"},
		"volume":    {"driver": "local", "driverOpts": map[string]any{"type": "none", "o": "bind", "device": "/mnt/kv"}},
	}}
	machine, err := patchedKV(t, pass)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	if len(pass.seen.Resources) != 1 || pass.seen.Resources[0] != (transform.Resource{Type: "kv", Name: "cache"}) {
		t.Errorf("the transform was offered %v, want the kv store being provisioned", pass.seen.Resources)
	}
	runCommand := machine.commands()[machine.at("'docker' 'run'")]
	for _, want := range []string{"'--save' '3600 1 300 100 60 10000' '--io-threads' '4'", "'--memory' '1g'", "'--maxmemory' '268435456'"} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the patched store was started without %q:\n%s", want, runCommand)
		}
	}
	if kept := machine.commands()[machine.at("'docker' 'volume' 'create'")]; !strings.Contains(kept, "device=/mnt/kv") {
		t.Errorf("the volume was created as %q, want the patched driver options", kept)
	}
}

func TestATransformSettingWhatOcelRendersForAKVStoreIsRefused(t *testing.T) {
	t.Parallel()

	for name, patch := range map[string]map[string]any{
		"an owned arg":       {"args": []any{"--maxmemory", "4gb"}},
		"the password":       {"args": []any{"--requirepass", "x"}},
		"extra flags in env": {"env": map[string]any{"VALKEY_EXTRA_FLAGS": "--user default on nopass"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine, err := patchedKV(t, &patching{patches: transform.Patches{"container": patch}})
			if err == nil || !strings.Contains(err.Error(), "cache") {
				t.Fatalf("ProvisionKV() = %v, want the patch refused naming the store", err)
			}
			if machine.at("'docker' 'run'") >= 0 {
				t.Error("the store started with the refused patch")
			}
		})
	}
}
