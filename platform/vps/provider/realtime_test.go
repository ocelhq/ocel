package vps_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const (
	realtimeGateway  = "shop-prod-infra-ocel-realtime"
	realtimeKeyKept  = "shop-prod-infra-realtime-app-signing-key"
	recordedRealtime = "cmVjb3JkZWQgc2lnbmluZyBrZXkgb2YgMzIgYnl0ZXM="
)

func aRealtime(t *testing.T, name string) resources.ProvisionRequest {
	t.Helper()
	stack, err := naming.ParseStackName("prod--infra")
	if err != nil {
		t.Fatal(err)
	}
	return resources.ProvisionRequest{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Resource: provider.Resource{
			Name: "realtime--" + name, Declared: name, Type: provider.BindingRealtime,
			Realtime: &provider.RealtimeSpec{
				TokenTTL: time.Minute,
				Channels: []provider.ChannelSpec{{Pattern: "orders/:orderId", Subscribe: realtime.ChannelAccessRule, Publish: realtime.ChannelAccessServer}},
			},
		},
	}
}

func gatewayEnv(t *testing.T, machine *box) map[string]string {
	t.Helper()
	var written string
	for at, command := range machine.commands() {
		if strings.Contains(command, "install -m 0600 /dev/stdin") && strings.Contains(command, realtimeGateway) {
			written = machine.feeds()[at]
		}
	}
	if written == "" {
		t.Fatalf("the gateway was handed no environment:\n%s", strings.Join(machine.commands(), "\n"))
	}
	env := map[string]string{}
	for line := range strings.Lines(written) {
		name, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		env[name] = value
	}
	return env
}

func gatewayKeys(t *testing.T, machine *box) map[string]string {
	t.Helper()
	var keys map[string]string
	if err := json.Unmarshal([]byte(gatewayEnv(t, machine)["OCEL_REALTIME_KEYS"]), &keys); err != nil {
		t.Fatalf("the gateway's keys are no JSON object: %v", err)
	}
	return keys
}

func TestAProviderOverABoxServesRealtime(t *testing.T) {
	t.Parallel()

	if served := over(&box{}).Facts().Bindings; !slices.Contains(served, provider.BindingRealtime) {
		t.Errorf("Facts().Bindings = %v, and a project declaring realtime is refused at deploy on a box", served)
	}
}

func TestADeclaredRealtimeIsBoundToItsEnvironmentsGatewayThroughTheAppsOwnOrigin(t *testing.T) {
	t.Parallel()

	p, _ := recording(&box{})
	binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind to: %v", err)
	}
	if binding.Type != provider.BindingRealtime || binding.Name != "realtime--app" || binding.Resource != "app" {
		t.Errorf("the binding is %s %q of %q, want the realtime the app declared", binding.Type, binding.Name, binding.Resource)
	}
	for property, want := range map[string]string{
		provider.PropertyURL:  boxapi.RealtimeSocketPath,
		provider.PropertyHost: realtimeGateway + ":8080",
	} {
		if got := binding.Properties[property]; got != want {
			t.Errorf("the binding's %s is %q, want %q", property, got, want)
		}
	}
	seed, _ := base64.StdEncoding.DecodeString(binding.Properties[provider.PropertySigningKey])
	public, _ := base64.StdEncoding.DecodeString(binding.Properties[provider.PropertyVerifyKey])
	if len(seed) != ed25519.SeedSize || !ed25519.PublicKey(public).Equal(ed25519.NewKeyFromSeed(seed).Public()) {
		t.Errorf("the binding's verify key is not its signing key's public key")
	}
}

func TestTheGatewayRunsAsOneConfinedContainerOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	run := machine.at("'docker' 'run'")
	if run < 0 {
		t.Fatalf("nothing was started:\n%s", strings.Join(machine.commands(), "\n"))
	}
	runCommand := machine.commands()[run]
	for _, want := range []string{
		"'--name' '" + realtimeGateway + "'",
		"'--network' 'ocel-production-shop'",
		"'--cap-drop' 'ALL'",
		"'--read-only'",
		"'--user' '65532:65532'",
		"source=" + host.RealtimeDir,
		"readonly",
		docker.StaticImage,
	} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the gateway was started without %q:\n%s", want, runCommand)
		}
	}
	for _, refused := range []string{"--publish", "'-p'", "--cap-add", "type=volume", "ocel.backup"} {
		if strings.Contains(runCommand, refused) {
			t.Errorf("the gateway was started with %s:\n%s", refused, runCommand)
		}
	}
	if machine.at("'docker' 'volume' 'create'") >= 0 {
		t.Errorf("a volume was made for the gateway, which keeps nothing:\n%s", strings.Join(machine.commands(), "\n"))
	}
	env := gatewayEnv(t, machine)
	if env["OCEL_REALTIME_HOST"] != binding.Properties[provider.PropertyHost] {
		t.Errorf("the gateway checks tokens for %q, and the app mints them for %q", env["OCEL_REALTIME_HOST"], binding.Properties[provider.PropertyHost])
	}
	if keys := gatewayKeys(t, machine); keys["app"] != binding.Properties[provider.PropertyVerifyKey] || len(keys) != 1 {
		t.Errorf("the gateway verifies %v, want app's verify key alone", keys)
	}
}

func TestTheGatewayHoldsNoMoreSocketsThanItsMemoryLimitHasRoomFor(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	if _, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard()); err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	runCommand := machine.commands()[machine.at("'docker' 'run'")]
	if !strings.Contains(runCommand, "'--memory' '"+strconv.Itoa(vps.RealtimeGatewayMemoryBytes)+"'") {
		t.Fatalf("the gateway was started with no memory limit, so a flood of sockets grows it until the box runs out:\n%s", runCommand)
	}
	sockets, err := strconv.Atoi(gatewayEnv(t, machine)[gatewayenv.MaxSocketsVar])
	if err != nil || sockets <= 0 {
		t.Fatalf("the gateway was handed %s=%q, so it accepts sockets without limit", gatewayenv.MaxSocketsVar, gatewayEnv(t, machine)[gatewayenv.MaxSocketsVar])
	}
	if sockets*vps.RealtimeSocketBytes > vps.RealtimeGatewayMemoryBytes {
		t.Errorf("the gateway holds up to %d sockets of %d bytes each in a %d-byte limit, so it is killed for memory before it refuses one", sockets, vps.RealtimeSocketBytes, vps.RealtimeGatewayMemoryBytes)
	}
}

func TestASigningKeyReachesTheBoxOnlySealedAndNeverOnACommandLine(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	signing := binding.Properties[provider.PropertySigningKey]
	for at, command := range machine.commands() {
		if strings.Contains(command, signing) || strings.Contains(machine.feeds()[at], signing) && strings.Contains(command, "/kept/") {
			t.Fatalf("the signing key reached the box unsealed:\n%s", command)
		}
		if strings.Contains(command, "install -m 0600 /dev/stdin") && strings.Contains(machine.feeds()[at], signing) {
			t.Fatalf("the gateway was handed the signing key, and it verifies with the public key alone:\n%s", command)
		}
	}
	if machine.at(container.KeptPath(environment.TierProduction, realtimeKeyKept)) < 0 {
		t.Errorf("the signing key is not kept on the box:\n%s", strings.Join(machine.commands(), "\n"))
	}
	if !strings.Contains(strings.Join(machine.commands(), "\n"), "13:realtime--app,") {
		t.Error("the signing key is not sealed to the realtime it belongs to")
	}
}

func TestASecondDeployBindsTheSigningKeyTheBoxKept(t *testing.T) {
	t.Parallel()

	machine := &box{}
	sealed := fakeSeal + base64.StdEncoding.EncodeToString([]byte(recordedRealtime))
	machine.kept = base64.StdEncoding.EncodeToString([]byte(sealed)) + "\n"
	p, _ := recording(machine)
	binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	if got := binding.Properties[provider.PropertySigningKey]; got != recordedRealtime {
		t.Errorf("the binding signs with %q, want the key the box kept, so tokens minted before the deploy still verify", got)
	}
}

func TestEveryRealtimeOfAnEnvironmentIsVerifiedByOneGateway(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	for _, name := range []string{"app", "chat"} {
		if _, err := p.ProvisionRealtime(context.Background(), aRealtime(t, name), progress.Discard()); err != nil {
			t.Fatalf("ProvisionRealtime(%s) = %v", name, err)
		}
	}
	if keys := gatewayKeys(t, machine); len(keys) != 2 || keys["app"] == "" || keys["chat"] == "" {
		t.Errorf("the gateway verifies %v, want app and chat", keys)
	}
}

func TestARemovedRealtimeIsDroppedFromTheGatewayAndTheLastTakesTheGatewayWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, _ := recording(machine)
	bindings := map[string]provider.Binding{}
	for _, name := range []string{"app", "chat"} {
		binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, name), progress.Discard())
		if err != nil {
			t.Fatalf("ProvisionRealtime(%s) = %v", name, err)
		}
		bindings[name] = binding
	}
	ref := aRealtime(t, "app").Ref

	if err := p.RemoveRealtime(context.Background(), ref, bindings["chat"], progress.Discard()); err != nil {
		t.Fatalf("RemoveRealtime(chat) = %v", err)
	}
	if keys := gatewayKeys(t, machine); len(keys) != 1 || keys["app"] == "" {
		t.Errorf("after chat is removed the gateway verifies %v, want app alone", keys)
	}
	if machine.at("rm -f '"+container.KeptPath(environment.TierProduction, "shop-prod-infra-realtime-chat-signing-key")+"'") < 0 {
		t.Errorf("chat's signing key is still kept:\n%s", strings.Join(machine.commands(), "\n"))
	}
	if machine.at("docker rm --force '"+realtimeGateway+"'") >= 0 {
		t.Fatalf("the gateway was taken down while app still declares realtime")
	}

	if err := p.RemoveRealtime(context.Background(), ref, bindings["app"], progress.Discard()); err != nil {
		t.Fatalf("RemoveRealtime(app) = %v", err)
	}
	if machine.at("docker rm --force '"+realtimeGateway+"'") < 0 {
		t.Errorf("the gateway outlived the last realtime of its environment:\n%s", strings.Join(machine.commands(), "\n"))
	}
}

func TestARealtimeWithNoConfigIsRefused(t *testing.T) {
	t.Parallel()

	in := aRealtime(t, "app")
	in.Resource.Realtime = nil
	p, _ := recording(&box{})
	_, err := p.ProvisionRealtime(context.Background(), in, progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("ProvisionRealtime() = %v, want a realtime with no config refused as invalid", err)
	}
}

func TestAnAppBindingRealtimeIsHandedItsEnvironmentsGatewayToPublishTo(t *testing.T) {
	t.Parallel()

	p, _ := recording(&box{})
	binding, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{binding}}
	manifest := manifestFor(t, &box{}, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}}, app)
	if want := "http://" + realtimeGateway + ":8080/publish"; manifest.RealtimePublishURL != want {
		t.Errorf("the runtime publishes realtime to %q, want the gateway of the environment's infra stack at %q", manifest.RealtimePublishURL, want)
	}
}

func TestAnAppBindingNoRealtimeIsHandedNoGateway(t *testing.T) {
	t.Parallel()

	manifest := manifestFor(t, &box{kept: sealedRootKey()}, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}}, boundApp())
	if manifest.RealtimePublishURL != "" {
		t.Errorf("an app binding no realtime is handed the gateway %q", manifest.RealtimePublishURL)
	}
}

type deployArrivingMidRun struct {
	records.Store
	once   sync.Once
	always bool
	arrive func(context.Context)
}

func (s *deployArrivingMidRun) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	listed, err := s.Store.List(ctx, in, under...)
	if in.Root == boxapi.RootRealtime && s.always {
		s.arrive(ctx)
	} else if in.Root == boxapi.RootRealtime {
		s.once.Do(func() { s.arrive(ctx) })
	}
	return listed, err
}

func TestAGatewayWhoseRealtimeKeepsChangingUnderItIsRefusedAsBusy(t *testing.T) {
	t.Parallel()

	p, store := recording(&box{})
	ref := aRealtime(t, "app").Ref
	record, err := json.Marshal(boxapi.RealtimeNamespace{VerifyKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))})
	if err != nil {
		t.Fatal(err)
	}
	arrived := 0
	p.Recording(&deployArrivingMidRun{Store: store, always: true, arrive: func(ctx context.Context) {
		arrived++
		key := boxapi.RealtimeNamespaceKey(ref.Tier, ref.Project, ref.Name.Env, fmt.Sprintf("chat%d", arrived))
		if _, err := store.Write(ctx, records.Entry{Key: key, Value: record}); err != nil {
			t.Errorf("record chat%d: %v", arrived, err)
		}
	}})

	_, err = p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("ProvisionRealtime() = %v, want it refused as busy rather than restarting the gateway without end", err)
	}
}

func TestARealtimeAnotherDeployRecordsWhileTheGatewayRestartsIsVerifiedByTheGatewayLeftRunning(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p, store := recording(machine)
	ref := aRealtime(t, "app").Ref
	chat, err := json.Marshal(boxapi.RealtimeNamespace{VerifyKey: base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))})
	if err != nil {
		t.Fatal(err)
	}
	p.Recording(&deployArrivingMidRun{Store: store, arrive: func(ctx context.Context) {
		key := boxapi.RealtimeNamespaceKey(ref.Tier, ref.Project, ref.Name.Env, "chat")
		if _, err := store.Write(ctx, records.Entry{Key: key, Value: chat}); err != nil {
			t.Errorf("record chat: %v", err)
		}
	}})

	if _, err := p.ProvisionRealtime(context.Background(), aRealtime(t, "app"), progress.Discard()); err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	if keys := gatewayKeys(t, machine); len(keys) != 2 || keys["app"] == "" || keys["chat"] == "" {
		t.Errorf("the gateway left running verifies %v, want app and the chat another deploy recorded meanwhile", keys)
	}
}

func plannedRealtime(t *testing.T, machine *box, store *fake.KeyValues, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string) {
	t.Helper()
	mark, records := len(machine.commands()), store.Snapshot()
	p := over(machine)
	p.Recording(store)
	action, reason, err := p.PlanRealtime(context.Background(), in, recorded)
	if err != nil {
		t.Fatalf("PlanRealtime() = %v", err)
	}
	refuseWrites(t, machine.since(mark))
	if !maps.Equal(store.Snapshot(), records) {
		t.Errorf("planning a realtime wrote a record, and a plan records nothing")
	}
	return action, reason
}

func realtimeProvisioned(t *testing.T, machine *box, store *fake.KeyValues, in resources.ProvisionRequest) provider.Binding {
	t.Helper()
	p := over(machine)
	p.Recording(store)
	binding, err := p.ProvisionRealtime(context.Background(), in, progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionRealtime() = %v", err)
	}
	return binding
}

func TestAnUnchangedRealtimePlansKeep(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	recorded := realtimeProvisioned(t, machine, store, aRealtime(t, "app"))
	if action, reason := plannedRealtime(t, machine, store, aRealtime(t, "app"), &recorded); action != provider.ActionKeep {
		t.Errorf("a realtime declared as it was provisioned plans %s %q, want keep", action, reason)
	}
}

func TestARealtimeNeverRecordedPlansACreate(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	if action, reason := plannedRealtime(t, machine, store, aRealtime(t, "app"), nil); action != provider.ActionCreate {
		t.Errorf("a realtime on a box that never ran one plans %s %q, want create", action, reason)
	}
}

func TestASecondRealtimePlansACreateThatRestartsTheGateway(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	realtimeProvisioned(t, machine, store, aRealtime(t, "app"))
	action, reason := plannedRealtime(t, machine, store, aRealtime(t, "chat"), nil)
	if action != provider.ActionCreate || !strings.Contains(reason, realtimeGateway) {
		t.Errorf("a second realtime plans %s %q, want a create saying the gateway is replaced to verify it", action, reason)
	}
}

func TestARealtimeWhoseGatewayStoppedPlansAnUpdate(t *testing.T) {
	t.Parallel()

	machine, store := &box{remembers: true}, fake.NewKeyValues()
	recorded := realtimeProvisioned(t, machine, store, aRealtime(t, "app"))
	machine.containers[realtimeGateway] = strings.Replace(machine.containers[realtimeGateway], "running", "exited", 1)
	action, reason := plannedRealtime(t, machine, store, aRealtime(t, "app"), &recorded)
	if action != provider.ActionUpdate || !strings.Contains(reason, realtimeGateway) {
		t.Errorf("a realtime whose gateway exited plans %s %q, and the apply starts a new gateway", action, reason)
	}
}
