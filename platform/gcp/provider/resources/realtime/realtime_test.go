package realtime

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

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime/pubsubbus"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

type realtimeHarness struct {
	run     *googletest.Run
	secrets *googletest.Secrets
	pubsub  *googletest.PubSub
	env     realtimeEnvironment

	mu          sync.Mutex
	pushed      []string
	writes      []string
	unaccounted bool
}

func newRealtimeHarness(t *testing.T) *realtimeHarness {
	t.Helper()
	h := &realtimeHarness{run: &googletest.Run{}, secrets: googletest.NewSecrets(), pubsub: googletest.NewPubSub()}
	serveRun := h.run.Serve(t)
	servePubSub := h.pubsub.Serve(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			h.mu.Lock()
			h.writes = append(h.writes, r.Method+" "+r.URL.Path)
			h.mu.Unlock()
		}
		if googletest.IsPubSubPath(r.URL.Path) {
			servePubSub(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/projects/") && strings.Contains(r.URL.Path, "/secrets") {
			h.secrets.Serve(t, w, r)
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/serviceAccounts/") {
			h.serveAccount(w, r)
			return
		}
		serveRun(w, r)
	}))
	t.Cleanup(server.Close)
	clients := googletest.Clients(server.URL)
	records := fake.NewKeyValues()
	services := &run.Services{OpenClients: googletest.Opener(clients), Records: records}
	h.env = realtimeEnvironment{
		clients:        clients,
		records:        records,
		ref:            googletest.ProductionRef(t),
		deployAndRoute: services.DeployAndRoute,
		tearDown:       services.TearDown,
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

func (h *realtimeHarness) serveAccount(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if h.unaccounted {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"Unknown service account"}}`))
		return
	}
	w.Write([]byte(`{"email":"` + r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] + `"}`))
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
	raw, found := h.secrets.Latest(t, h.env.clients.RealtimeKeysSecret(environment.TierProduction, "shop", "prod"))
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
	service := h.run.Serving()
	if service == nil {
		t.Fatal("no gateway service was created")
	}
	host := strings.TrimPrefix(service.Uri, "https://")
	for property, want := range map[string]string{
		provider.PropertyURL:  "wss://" + host + "/event/realtime",
		provider.PropertyHost: host,
	} {
		if got := binding.Properties[property]; got != want {
			t.Errorf("the binding's %s is %q, want %q", property, got, want)
		}
	}
	if got := googletest.EnvOf(service.Template.Containers[0])[gatewayenv.HostVar]; got != host {
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

func (h *realtimeHarness) topic() string {
	return "projects/acme-prod/topics/" + h.env.clients.RealtimeGateway("shop", "prod")
}

func TestARealtimeGatewaysEnvironmentHasATopicItsAccountMayPublishOnAndSubscribeTo(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")

	if !h.pubsub.HasTopic(h.topic()) {
		t.Fatalf("no topic %s was created, and the gateway's instances hear each other's publishes through it alone", h.topic())
	}
	member := "serviceAccount:" + h.env.clients.RealtimeAccountEmail(environment.TierProduction)
	for _, role := range []string{"roles/pubsub.publisher", "roles/pubsub.subscriber"} {
		if members := h.pubsub.TopicMembers(h.topic(), role); !slices.Equal(members, []string{member}) {
			t.Errorf("the topic binds %v to %s, want the tier's realtime account alone", members, role)
		}
	}
	if got := googletest.EnvOf(h.run.Serving().Template.Containers[0])[pubsubbus.TopicVar]; got != h.topic() {
		t.Errorf("the gateway hears %q, want the topic %s", got, h.topic())
	}
}

func TestADeployThatChangesNoRealtimeWritesNothing(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	versions, releases := h.secrets.VersionsAdded(), len(h.run.Releases())
	h.mu.Lock()
	written := len(h.writes)
	h.mu.Unlock()
	h.provision(t, "app")

	if got := h.secrets.VersionsAdded(); got != versions {
		t.Errorf("a deploy changing nothing added %d secret versions", got-versions)
	}
	if got := len(h.run.Releases()); got != releases {
		t.Errorf("a deploy changing nothing released the gateway %d times", got-releases)
	}
	if got := h.pushes(); len(got) != 1 {
		t.Errorf("the gateway image was pushed %d times, want once: the service already runs it", len(got))
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := h.writes[written:]; len(got) != 0 {
		t.Errorf("a deploy changing nothing wrote %q", got)
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
	if h.secrets.Has(h.env.clients.RealtimeSigningSecret("shop", "prod", "chat")) {
		t.Error("chat's signing key outlived it")
	}
	if h.run.Serving() == nil {
		t.Error("the gateway was taken down while app still uses it")
	}
}

func TestRemovingTheLastRealtimeTakesTheGatewayAndItsKeysAway(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.remove(t, h.provision(t, "app"))

	if h.run.Serving() != nil {
		t.Error("the gateway outlived the last realtime it served")
	}
	if h.pubsub.HasTopic(h.topic()) {
		t.Errorf("the topic %s outlived the gateway it carried publishes between", h.topic())
	}
	for _, secret := range []string{h.env.clients.RealtimeKeysSecret(environment.TierProduction, "shop", "prod"), h.env.clients.RealtimeSigningSecret("shop", "prod", "app")} {
		if h.secrets.Has(secret) {
			t.Errorf("secret %s outlived the last realtime", secret)
		}
	}
}

func TestAnEmulatedEnvironmentHandsItsGatewayEveryKeyInItsEnvironment(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.env.keysInEnv = true
	app := h.provision(t, "app")

	var keys map[string]string
	if err := json.Unmarshal([]byte(googletest.EnvOf(h.run.Serving().Template.Containers[0])[gatewayenv.KeysVar]), &keys); err != nil {
		t.Fatalf("the gateway's %s is no JSON object: %v", gatewayenv.KeysVar, err)
	}
	if len(keys) != 1 || keys["app"] != app.Properties[provider.PropertyVerifyKey] {
		t.Errorf("the gateway's keys are %v, want app's alone", keys)
	}
}

func (h *realtimeHarness) plan(t *testing.T, name string) (provider.ChangeAction, string, error) {
	t.Helper()
	h.mu.Lock()
	written, pushed := len(h.writes), len(h.pushed)
	h.mu.Unlock()
	key := h.env.namespaceKey(name)
	before, _ := records.ReadOrEmpty(context.Background(), h.env.records, key)

	action, reason, err := h.env.plan(context.Background(), aRealtime(name))

	h.mu.Lock()
	if got := h.writes[written:]; len(got) != 0 {
		t.Errorf("plan(%s) wrote %q, want a plan that only reads", name, got)
	}
	if got := h.pushed[pushed:]; len(got) != 0 {
		t.Errorf("plan(%s) pushed the gateway image %q, want a plan that only reads", name, got)
	}
	h.mu.Unlock()
	if after, _ := records.ReadOrEmpty(context.Background(), h.env.records, key); string(after.Value) != string(before.Value) {
		t.Errorf("plan(%s) changed the realtime's record, want a plan that only reads", name)
	}
	return action, reason, err
}

func TestAMissingRealtimePlansACreate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	action, _, err := h.plan(t, "app")
	if err != nil || action != provider.ActionCreate {
		t.Errorf("plan() = %s, %v, want %s", action, err, provider.ActionCreate)
	}
	if h.secrets.Has(h.env.clients.RealtimeSigningSecret("shop", "prod", "app")) {
		t.Error("plan() minted a signing key, want a plan that only reads")
	}
}

func TestARealtimeAsProvisionedPlansKeep(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.provision(t, "chat")

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionKeep || reason != provider.ReasonCurrent {
		t.Errorf("plan() = %s %q, %v, want %s %q", action, reason, err, provider.ActionKeep, provider.ReasonCurrent)
	}
}

func TestARealtimeWhoseGatewayDriftedPlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.run.Mu.Lock()
	h.run.Service.Template.MaxInstanceRequestConcurrency = 80
	h.run.Mu.Unlock()

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "released again") {
		t.Errorf("plan() = %s %q, %v, want an update saying the gateway is released again", action, reason, err)
	}
}

func TestARealtimeWhoseGatewayIsGonePlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.run.Mu.Lock()
	h.run.Service = nil
	h.run.Mu.Unlock()

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "is created") {
		t.Errorf("plan() = %s %q, %v, want an update saying the gateway is created", action, reason, err)
	}
}

func TestARealtimeWhoseTopicIsGonePlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.pubsub.Mu.Lock()
	delete(h.pubsub.Topics, h.topic())
	h.pubsub.Mu.Unlock()

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, h.topic()) {
		t.Errorf("plan() = %s %q, %v, want an update naming the topic %s that is created", action, reason, err, h.topic())
	}
	h.provision(t, "app")
	if !h.pubsub.HasTopic(h.topic()) {
		t.Errorf("a deploy left the topic %s missing", h.topic())
	}
}

func TestARealtimeWhoseTopicLostItsGrantsPlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.pubsub.AddTopic(h.topic())

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "publish") {
		t.Errorf("plan() = %s %q, %v, want an update saying the realtime account may publish and subscribe again", action, reason, err)
	}
}

func TestARealtimeWhoseRecordWasLostPlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	if err := records.Forget(context.Background(), h.env.records, h.env.namespaceKey("app")); err != nil {
		t.Fatal(err)
	}

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "verify key") {
		t.Errorf("plan() = %s %q, %v, want an update saying the verify key is recorded", action, reason, err)
	}
}

func TestARealtimeWhoseKeysChangedPlansAnUpdate(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	stale, err := json.Marshal(realtimeNamespace{VerifyKey: "c3RhbGU="})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Change(context.Background(), h.env.records, h.env.namespaceKey("chat"), func(records.Entry) ([]byte, bool, error) {
		return stale, true, nil
	}); err != nil {
		t.Fatal(err)
	}

	action, reason, err := h.plan(t, "app")
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "chat") {
		t.Errorf("plan() = %s %q, %v, want an update naming the realtimes the gateway's keys secret comes to hold", action, reason, err)
	}
}

func TestARealtimeIsPricedAsAGatewayOfUpToTenInstancesItsTopicAndTheirSubscriptions(t *testing.T) {
	t.Parallel()

	tree := &pricing.Tree{}
	scope := tree.Scope("", pricing.ScopeProject, "shop")
	named := names.New(provider.Namespace("ocel"), "acme-prod")
	shape := component.Shape{
		Tree: tree, Vendor: "gcp", Names: named, Region: "europe-west1", Environment: scope,
		Request: provider.ShapeRequest{Deploy: provider.DeploySpec{Slug: "shop", Env: "prod", Tier: environment.TierProduction}},
	}
	if err := shapeRealtime(shape, []provider.Resource{aRealtime("app")}); err != nil {
		t.Fatalf("shapeRealtime() = %v", err)
	}
	set, err := tree.Set("ocel")
	if err != nil {
		t.Fatal(err)
	}
	gateway := named.RealtimeGateway("shop", "prod")
	found := map[string]*costv1.Resource{}
	for _, resource := range set.Resources {
		found[resource.Type+":"+resource.Name] = resource
	}
	for _, want := range []string{cost.PubSubTopicType + ":" + gateway, cost.PubSubSubscriptionType + ":" + gateway} {
		if found[want] == nil {
			t.Errorf("the shape prices no %s, and every publish the gateway carries crosses it", want)
		}
	}
	service := found[cost.CloudRunServiceType+":"+gateway]
	if service == nil {
		t.Fatalf("the shape prices no gateway service %s", gateway)
	}
	scaling := service.Properties.AsMap()["template"].(map[string]any)["scaling"].(map[string]any)
	if scaling["max_instance_count"] != float64(10) {
		t.Errorf("the gateway is priced at up to %v instances, want 10", scaling["max_instance_count"])
	}
}
