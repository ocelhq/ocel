package kv_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker/dockertest"
	"github.com/ocelhq/ocel/cli/internal/devresources/kv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func declared(name string, config *resourcesv1.KvConfig) declaration.Resource {
	return declaration.Resource{Name: name, Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV, KV: config}
}

func bound(t *testing.T, raw string) *bindingsv1.KvProperties {
	t.Helper()
	var b bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("the binding is not the JSON the SDKs read: %v\n%s", err, raw)
	}
	return b.GetKv()
}

func argAfter(args []string, flag string) string {
	at := slices.Index(args, flag)
	if at < 0 || at+1 >= len(args) {
		return ""
	}
	return args[at+1]
}

func TestAStoreRunsInItsOwnPinnedValkeyContainerWithTheRenderedConfiguration(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	resolved, err := kv.New(engine.OpenFunc(), t.TempDir()).Resolve(context.Background(), "shop-1a2b", []declaration.Resource{
		declared("cache", &resourcesv1.KvConfig{Eviction: "allkeys-lru"}),
		declared("sessions", &resourcesv1.KvConfig{Version: "8", Memory: "1gb"}),
	})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if len(engine.Specs) != 2 {
		t.Fatalf("ran %d containers, want one per store", len(engine.Specs))
	}
	cache, sessions := engine.Specs[0], engine.Specs[1]
	if !strings.HasPrefix(cache.Image, "valkey/valkey:9.") || !strings.Contains(cache.Image, "@sha256:") {
		t.Errorf("cache runs %q, want valkey 9 pinned by digest when no version is declared", cache.Image)
	}
	if !strings.HasPrefix(sessions.Image, "valkey/valkey:8.") {
		t.Errorf("sessions runs %q, want the valkey 8 it declares", sessions.Image)
	}
	if got := argAfter(cache.Args, "--maxmemory"); got != "268435456" {
		t.Errorf("cache --maxmemory = %q, want the 256mb default in bytes", got)
	}
	if got := argAfter(cache.Args, "--maxmemory-policy"); got != "allkeys-lru" {
		t.Errorf("cache --maxmemory-policy = %q, want the declared eviction", got)
	}
	if got := argAfter(sessions.Args, "--maxmemory"); got != "1073741824" {
		t.Errorf("sessions --maxmemory = %q, want 1gb in bytes", got)
	}
	if cache.User != "valkey:valkey" || cache.Port != 6379 || cache.VolumePath != "/data" {
		t.Errorf("cache runs as %q on %d with data at %q, want valkey:valkey, 6379 and /data", cache.User, cache.Port, cache.VolumePath)
	}
	if cache.Volume == "" || cache.Volume == sessions.Volume || !strings.Contains(cache.Volume, "shop-1a2b") {
		t.Errorf("volumes %q and %q, want one per store named for the project", cache.Volume, sessions.Volume)
	}
	if cache.Labels["dev.ocel.project"] != "shop-1a2b" || cache.Labels["dev.ocel.backend"] != "kv" {
		t.Errorf("Labels = %v, want the project and the backend", cache.Labels)
	}

	if resolved[0].Origin != "valkey:9 @ 127.0.0.1:54001" {
		t.Errorf("Origin = %q", resolved[0].Origin)
	}
	props := bound(t, resolved[0].Env["OCEL_RESOURCE_KV_cache"])
	if props.GetHost() != "127.0.0.1" || props.GetPort() != 54001 || props.GetUsername() != "ocel" || props.GetPassword() == "" || props.GetTls() {
		t.Errorf("binding = %s:%d as %q, password set %t, tls %t, want the container's address, user ocel, a password and no TLS",
			props.GetHost(), props.GetPort(), props.GetUsername(), props.GetPassword() != "", props.GetTls())
	}
	if other := bound(t, resolved[1].Env["OCEL_RESOURCE_KV_sessions"]).GetPassword(); other == props.GetPassword() {
		t.Error("two stores share one password, and a store is its own engine with its own")
	}
}

func TestTheBindingIsHandedOutOnlyOnceTheStoreAuthenticatesItsUser(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	answered := 0
	engine.Answer = func(argv []string) (string, error) {
		if argv[0] == "valkey-cli" {
			answered++
			if answered < 3 {
				return "", errors.New("LOADING Valkey is loading the dataset in memory")
			}
		}
		return "", nil
	}
	resolved, err := kv.New(engine.OpenFunc(), t.TempDir()).Resolve(context.Background(), "shop", []declaration.Resource{declared("cache", &resourcesv1.KvConfig{})})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if answered != 3 {
		t.Errorf("the store was probed %d times, want until it answered", answered)
	}
	password := bound(t, resolved[0].Env["OCEL_RESOURCE_KV_cache"]).GetPassword()
	for at, argv := range engine.Execs {
		joined := dockertest.Joined(argv)
		if strings.Contains(joined, password) {
			t.Fatalf("the password rode argv: %v", argv)
		}
		if argv[0] == "valkey-cli" && (!strings.Contains(joined, "--askpass") || !strings.Contains(engine.Inputs[at], password)) {
			t.Errorf("probe %v was fed %q, want it to authenticate with the password on stdin", argv, engine.Inputs[at])
		}
	}
	for _, spec := range engine.Specs {
		if strings.Contains(strings.Join(spec.Args, " "), password) || strings.Contains(strings.Join(spec.Env, " "), password) {
			t.Errorf("the container was started with the password where docker inspect shows it: %+v", spec)
		}
	}
}

func TestAStoreThatNeverAnswersIsNotBound(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	ctx, cancel := context.WithCancel(context.Background())
	engine.Answer = func(argv []string) (string, error) {
		cancel()
		return "", errors.New("Could not connect")
	}
	if _, err := kv.New(engine.OpenFunc(), t.TempDir()).Resolve(ctx, "shop", []declaration.Resource{declared("cache", &resourcesv1.KvConfig{})}); err == nil || !strings.Contains(err.Error(), `"cache"`) {
		t.Fatalf("Resolve = %v, want the store named as never answering", err)
	}
}

func TestTwoProcessesOfOneProjectHandOutTheSameStorePassword(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	var passwords []string
	for range 2 {
		engine := &dockertest.Engine{}
		resolved, err := kv.New(engine.OpenFunc(), state).Resolve(context.Background(), "shop", []declaration.Resource{declared("cache", &resourcesv1.KvConfig{})})
		if err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		passwords = append(passwords, bound(t, resolved[0].Env["OCEL_RESOURCE_KV_cache"]).GetPassword())
		if hashed := engine.Specs[0].Args; !slices.ContainsFunc(hashed, func(arg string) bool { return strings.HasPrefix(arg, "#") }) {
			t.Fatalf("Args = %v, want the user known by its password's hash", hashed)
		}
	}
	if passwords[0] != passwords[1] {
		t.Fatalf("passwords = %v, want the one minted once and kept for the store", passwords)
	}
}

func TestASecondSyncReusesTheRunningStore(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	backend := kv.New(engine.OpenFunc(), t.TempDir())
	for range 2 {
		if _, err := backend.Resolve(context.Background(), "shop", []declaration.Resource{declared("cache", &resourcesv1.KvConfig{})}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
	}
	if len(engine.Specs) != 1 {
		t.Fatalf("ran %d containers across two syncs, want 1", len(engine.Specs))
	}
}

func TestAStoreDeclaredUnderAnotherVersionWhileDevRunsReplacesTheOldContainer(t *testing.T) {
	t.Parallel()

	engine := &dockertest.Engine{}
	backend := kv.New(engine.OpenFunc(), t.TempDir())
	for _, version := range []string{"8", "9"} {
		if _, err := backend.Resolve(context.Background(), "shop", []declaration.Resource{declared("cache", &resourcesv1.KvConfig{Version: version})}); err != nil {
			t.Fatalf("Resolve(%s) = %v", version, err)
		}
	}
	if len(engine.Specs) != 2 || !slices.Equal(engine.Stopped, []string{engine.Specs[0].Name}) {
		t.Fatalf("ran %d containers and stopped %v, want the version 8 store stopped once 9 runs", len(engine.Specs), engine.Stopped)
	}
	if err := backend.Close(context.Background(), true); err != nil || len(engine.Stopped) != 2 || engine.Stopped[1] != engine.Specs[1].Name {
		t.Errorf("Close = %v, stopped %v, want the version 9 store stopped", err, engine.Stopped)
	}
}

func TestAStoreDeclaringWhatNoStoreRunsIsRefusedBeforeAnythingStarts(t *testing.T) {
	t.Parallel()

	for _, config := range []*resourcesv1.KvConfig{
		{Version: "7"},
		{Eviction: "lru"},
		{Memory: "lots"},
	} {
		engine := &dockertest.Engine{}
		_, err := kv.New(engine.OpenFunc(), t.TempDir()).Resolve(context.Background(), "shop", []declaration.Resource{declared("cache", config)})
		if err == nil || !strings.Contains(err.Error(), `kv "cache"`) {
			t.Errorf("Resolve(%v) = %v, want a refusal naming the store", config, err)
		}
		if len(engine.Specs) != 0 {
			t.Errorf("a container started for %v", config)
		}
	}
}

func TestCloseStopsEveryStoreOnlyWhenNoOtherProcessUsesThem(t *testing.T) {
	t.Parallel()

	for _, last := range []bool{false, true} {
		engine := &dockertest.Engine{}
		backend := kv.New(engine.OpenFunc(), t.TempDir())
		if _, err := backend.Resolve(context.Background(), "shop", []declaration.Resource{
			declared("cache", &resourcesv1.KvConfig{}), declared("sessions", &resourcesv1.KvConfig{}),
		}); err != nil {
			t.Fatalf("Resolve = %v", err)
		}
		if err := backend.Close(context.Background(), last); err != nil {
			t.Fatalf("Close = %v", err)
		}
		if want := map[bool]int{false: 0, true: 2}[last]; len(engine.Stopped) != want {
			t.Errorf("Close(%v) stopped %v, want %d", last, engine.Stopped, want)
		}
	}
}
