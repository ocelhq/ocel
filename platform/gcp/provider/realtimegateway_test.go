package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

func productionRef(t *testing.T) provider.StackRef {
	t.Helper()
	stack, err := naming.ParseStackName("prod--infra")
	if err != nil {
		t.Fatal(err)
	}
	return provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack}
}

func gatewayService(t *testing.T, host string) *run.GoogleCloudRunV2Service {
	t.Helper()
	names := Names{namespace: provider.Namespace("ocel"), project: "acme-prod"}
	desired, err := serviceOf(realtimeGatewayServing(names, productionRef(t), host, "europe-west1-docker.pkg.dev/acme-prod/ocel-acme-prod-production/ocel-realtime:abc", ""))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	return desired
}

func envOf(container *run.GoogleCloudRunV2Container) map[string]string {
	env := map[string]string{}
	for _, entry := range container.Env {
		env[entry.Name] = entry.Value
	}
	return env
}

func TestTheGatewayServiceTakesBrowsersFromAnywhereWithoutCloudRunsInvokerCheck(t *testing.T) {
	t.Parallel()

	desired := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app")
	if desired.Ingress != ingressEverywhere {
		t.Errorf("Ingress = %q, want %q: browsers connect straight to the gateway", desired.Ingress, ingressEverywhere)
	}
	if !desired.InvokerIamDisabled {
		t.Error("InvokerIamDisabled = false, and a browser carries no Google identity to pass Cloud Run's invoker check")
	}
}

func TestTheGatewayRunsOneInstanceOfOneVCPUAndAGibibyteHoldingAThousandSocketsForAnHour(t *testing.T) {
	t.Parallel()

	template := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app").Template
	if template.Scaling.MinInstanceCount != 0 || template.Scaling.MaxInstanceCount != 1 {
		t.Errorf("instances = %d to %d, want 0 to 1: a second instance would hold sockets the first never publishes to",
			template.Scaling.MinInstanceCount, template.Scaling.MaxInstanceCount)
	}
	if template.MaxInstanceRequestConcurrency != 1000 {
		t.Errorf("concurrency = %d, want 1000", template.MaxInstanceRequestConcurrency)
	}
	if template.Timeout != "3600s" {
		t.Errorf("timeout = %q, want 3600s, the longest Cloud Run holds a socket", template.Timeout)
	}
	resources := template.Containers[0].Resources
	if !sameLimits(resources.Limits, map[string]string{"cpu": "1", "memory": "1Gi"}) {
		t.Errorf("limits = %v, want 1 vCPU and 1Gi", resources.Limits)
	}
	if !resources.CpuIdle {
		t.Error("CpuIdle = false, and the gateway is billed per request, which an open socket keeps active")
	}
}

func TestTheGatewayReadsItsKeysFromTheLatestVersionOfItsEnvironmentsKeysSecret(t *testing.T) {
	t.Parallel()

	desired := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app")
	names := Names{namespace: provider.Namespace("ocel"), project: "acme-prod"}
	if len(desired.Template.Volumes) != 1 {
		t.Fatalf("volumes = %d, want the keys secret alone", len(desired.Template.Volumes))
	}
	volume := desired.Template.Volumes[0]
	if volume.Secret == nil || volume.Secret.Secret != names.RealtimeKeysSecret(environment.TierProduction, "shop", "prod") {
		t.Fatalf("the volume mounts %+v, want secret %s", volume.Secret, names.RealtimeKeysSecret(environment.TierProduction, "shop", "prod"))
	}
	if items := volume.Secret.Items; len(items) != 1 || items[0].Version != "latest" {
		t.Errorf("the keys secret is mounted at %+v, want version latest so a new version is read without a new revision", items)
	}
	mount := desired.Template.Containers[0].VolumeMounts[0]
	env := envOf(desired.Template.Containers[0])
	if want := mount.MountPath + "/" + volume.Secret.Items[0].Path; env[gatewayenv.KeysFileVar] != want {
		t.Errorf("%s = %q, want the mounted file %q", gatewayenv.KeysFileVar, env[gatewayenv.KeysFileVar], want)
	}
	if env[gatewayenv.KeysVar] != "" {
		t.Errorf("%s = %q, want no keys in the environment, which a revision fixes", gatewayenv.KeysVar, env[gatewayenv.KeysVar])
	}
	if desired.Template.ServiceAccount != names.RealtimeAccountEmail(environment.TierProduction) {
		t.Errorf("the gateway runs as %q, want the tier's realtime account", desired.Template.ServiceAccount)
	}
}

func TestTheGatewayChecksTokensForItsOwnHostAdmitsAnyOriginAndCapsItsSockets(t *testing.T) {
	t.Parallel()

	env := envOf(gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app").Template.Containers[0])
	for name, want := range map[string]string{
		gatewayenv.HostVar:       "ocel-shop-prod-realtime-abc123-ew.a.run.app",
		gatewayenv.AnyOriginVar:  "true",
		gatewayenv.MaxSocketsVar: "950",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

func TestOnTheEmulatorTheGatewayTakesItsKeysFromItsEnvironmentForWantOfSecretVolumes(t *testing.T) {
	t.Parallel()

	names := Names{namespace: provider.Namespace("ocel"), project: "acme-prod"}
	keys := `{"app":"AAAA"}`
	desired, err := serviceOf(realtimeGatewayServing(names, productionRef(t), "gateway.run.app", "ocel-realtime:abc", keys))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	if len(desired.Template.Volumes) != 0 {
		t.Errorf("volumes = %d, want none: the emulator runs no secret volume", len(desired.Template.Volumes))
	}
	env := envOf(desired.Template.Containers[0])
	if env[gatewayenv.KeysVar] != keys || env[gatewayenv.KeysFileVar] != "" {
		t.Errorf("%s = %q and %s = %q, want the keys in the environment and no file", gatewayenv.KeysVar, env[gatewayenv.KeysVar], gatewayenv.KeysFileVar, env[gatewayenv.KeysFileVar])
	}
}

func TestATierBootstrappedBeforeRealtimeIsRefusedNamingTheBootstrapToRunAgain(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.unaccounted = true
	_, err := h.env.provision(context.Background(), aRealtime("app"), nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "`ocel bootstrap` for this tier again") {
		t.Errorf("provision() = %v, want a not-ready refusal saying to run `ocel bootstrap` for this tier again", err)
	}
	if h.run.serving() != nil {
		t.Error("a gateway was released to run as an account the tier does not have")
	}
}

func TestAGatewayCloudRunServesOnAnotherHostEachReleaseIsRefused(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.run.uriEachRevision = true
	_, err := h.env.provision(context.Background(), aRealtime("app"), nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("provision() = %v, want a busy refusal: no host the binding names would be the one browsers reach", err)
	}
}

func TestAGatewayReleasedAgainServesTheRevisionThatReleaseMade(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.run.mu.Lock()
	h.run.service.Template.Containers[0].Image += "-older"
	h.run.mu.Unlock()
	h.provision(t, "app")

	service := h.run.serving()
	if served, latest := servedRevision(service), revisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the gateway serves %q, want %q, the revision its release made: an upgraded gateway otherwise never takes a socket", served, latest)
	}
}

func TestAGatewayWhoseLatestRevisionServesNoTrafficIsRoutedToItOnTheNextDeploy(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.run.mu.Lock()
	h.run.service.Traffic = []*run.GoogleCloudRunV2TrafficTarget{{Type: trafficByRevision, Revision: "an-older-revision", Percent: 100}}
	h.run.mu.Unlock()
	h.provision(t, "app")

	service := h.run.serving()
	if served, latest := servedRevision(service), revisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the gateway serves %q, want %q: a release that came ready and was never routed is routed once the gateway is next deployed", served, latest)
	}
}
