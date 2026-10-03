package gcp

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

type realtimeHarness struct {
	run     *runServer
	secrets *secretServer
	env     realtimeEnvironment

	mu     sync.Mutex
	pushed []string
}

func newRealtimeHarness(t *testing.T) *realtimeHarness {
	t.Helper()
	h := &realtimeHarness{run: &runServer{}, secrets: newSecretServer()}
	serveRun := h.run.serve(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/projects/") && strings.Contains(r.URL.Path, "/secrets") {
			h.secrets.serve(t, w, r)
			return
		}
		serveRun(w, r)
	}))
	t.Cleanup(server.Close)
	p := pushing(t, server.URL)
	h.env = realtimeEnvironment{
		clients:       p.resolved,
		records:       fake.NewKeyValues(),
		ref:           productionRef(t),
		deployService: p.deployService,
		tearDown:      p.tearDown,
		pushBinary: func(_ context.Context, _ environment.Tier, _, ref string, binary []byte, _ string) error {
			if len(binary) == 0 {
				t.Error("the gateway image was pushed with no binary in it")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			h.pushed = append(h.pushed, ref)
			return nil
		},
	}
	return h
}

func aRealtime(name string) provider.Resource {
	return provider.Resource{
		Name: "realtime--" + name, Declared: name, Type: provider.BindingRealtime,
		Realtime: &provider.RealtimeSpec{
			TokenTTL: time.Minute,
			Channels: []provider.ChannelSpec{{Pattern: "orders/:orderId", Subscribe: realtime.ChannelAccessRule, Publish: realtime.ChannelAccessServer}},
		},
	}
}

func (h *realtimeHarness) provision(t *testing.T, name string) provider.Binding {
	t.Helper()
	binding, err := h.env.provision(context.Background(), aRealtime(name), nil)
	if err != nil {
		t.Fatalf("provision(%s) = %v", name, err)
	}
	return binding
}

func (h *realtimeHarness) remove(t *testing.T, binding provider.Binding) {
	t.Helper()
	if err := h.env.remove(context.Background(), binding, nil); err != nil {
		t.Fatalf("remove(%s) = %v", binding.Name, err)
	}
}

func (h *realtimeHarness) keys(t *testing.T) map[string]string {
	t.Helper()
	raw, found := h.secrets.latest(t, h.env.clients.RealtimeKeysSecret("shop", "prod"))
	if !found {
		t.Fatal("the keys secret holds no version")
	}
	var keys map[string]string
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		t.Fatalf("the keys secret holds %q, which is no JSON object: %v", raw, err)
	}
	return keys
}

func (h *realtimeHarness) pushes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.pushed)
}

func TestARealtimeIsBoundToItsEnvironmentsGatewayByTheHostCloudRunGaveIt(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	binding := h.provision(t, "app")

	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind to: %v", err)
	}
	if binding.Type != provider.BindingRealtime || binding.Name != "realtime--app" || binding.Resource != "app" {
		t.Errorf("the binding is %s %q of %q, want the realtime the app declared", binding.Type, binding.Name, binding.Resource)
	}
	service := h.run.serving()
	if service == nil {
		t.Fatal("no gateway service was created")
	}
	host := strings.TrimPrefix(service.Uri, "https://")
	for property, want := range map[string]string{
		provider.PropertyTransport: "REALTIME_TRANSPORT_OCEL_GATEWAY",
		provider.PropertyURL:       "wss://" + host + "/event/realtime",
		provider.PropertyHost:      host,
	} {
		if got := binding.Properties[property]; got != want {
			t.Errorf("the binding's %s is %q, want %q", property, got, want)
		}
	}
	if got := envOf(service.Template.Containers[0])[gatewayenv.HostVar]; got != host {
		t.Errorf("the gateway checks tokens for %q, want %q, the host the binding names", got, host)
	}
	seed, _ := base64.StdEncoding.DecodeString(binding.Properties[provider.PropertySigningKey])
	public, _ := base64.StdEncoding.DecodeString(binding.Properties[provider.PropertyVerifyKey])
	if len(seed) != ed25519.SeedSize || !ed25519.PublicKey(public).Equal(ed25519.NewKeyFromSeed(seed).Public()) {
		t.Error("the binding's verify key is not its signing key's public key")
	}
	if got := h.keys(t)["app"]; got != binding.Properties[provider.PropertyVerifyKey] {
		t.Errorf("the gateway's keys name %q for app, want the binding's verify key", got)
	}
}

func TestADeployThatChangesNoRealtimeWritesNothing(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	versions, releases := h.secrets.versionsAdded(), len(h.run.releases())
	h.provision(t, "app")

	if got := h.secrets.versionsAdded(); got != versions {
		t.Errorf("a deploy changing nothing added %d secret versions", got-versions)
	}
	if got := len(h.run.releases()); got != releases {
		t.Errorf("a deploy changing nothing released the gateway %d times", got-releases)
	}
	if got := h.pushes(); len(got) != 1 {
		t.Errorf("the gateway image was pushed %d times, want once: the service already runs it", len(got))
	}
}

func TestRemovingARealtimeStopsTrustingItAndDeletesItsSigningKey(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	app := h.provision(t, "app")
	chat := h.provision(t, "chat")
	h.remove(t, chat)

	if got := h.keys(t); len(got) != 1 || got["app"] != app.Properties[provider.PropertyVerifyKey] {
		t.Errorf("the keys are %v once chat is removed, want app's alone", got)
	}
	if h.secrets.has(h.env.clients.RealtimeSigningSecret("shop", "prod", "chat")) {
		t.Error("chat's signing key outlived it")
	}
	if h.run.serving() == nil {
		t.Error("the gateway was taken down while app still uses it")
	}
}

func TestRemovingTheLastRealtimeTakesTheGatewayAndItsKeysAway(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.remove(t, h.provision(t, "app"))

	if h.run.serving() != nil {
		t.Error("the gateway outlived the last realtime it served")
	}
	for _, secret := range []string{h.env.clients.RealtimeKeysSecret("shop", "prod"), h.env.clients.RealtimeSigningSecret("shop", "prod", "app")} {
		if h.secrets.has(secret) {
			t.Errorf("secret %s outlived the last realtime", secret)
		}
	}
}

func TestADeployCredentialMayKeepTheSecretsRealtimeIsServedFrom(t *testing.T) {
	t.Parallel()

	if !slices.Contains(rolesFor(edge.PurposeDeploy), "roles/secretmanager.admin") {
		t.Errorf("a deploy credential is granted %v, want roles/secretmanager.admin: a deploy creates, writes, deletes and sets who reads a realtime's secrets", rolesFor(edge.PurposeDeploy))
	}
}

func TestAnEmulatedEnvironmentHandsItsGatewayEveryKeyInItsEnvironment(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.env.keysInEnv = true
	app := h.provision(t, "app")

	var keys map[string]string
	if err := json.Unmarshal([]byte(envOf(h.run.serving().Template.Containers[0])[gatewayenv.KeysVar]), &keys); err != nil {
		t.Fatalf("the gateway's %s is no JSON object: %v", gatewayenv.KeysVar, err)
	}
	if len(keys) != 1 || keys["app"] != app.Properties[provider.PropertyVerifyKey] {
		t.Errorf("the gateway's keys are %v, want app's alone", keys)
	}
}
