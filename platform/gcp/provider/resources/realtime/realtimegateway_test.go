package realtime

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/realtime/pubsubbus"
	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

func servingOf(t *testing.T, named names.Names, ref provider.StackRef, host, image, inlineKeys, pubsubEndpoint string) run.Serving {
	t.Helper()
	service, err := named.RealtimeGateway(ref.Project, ref.Name.Env)
	if err != nil {
		t.Fatalf("RealtimeGateway() = %v", err)
	}
	return realtimeGatewayServing(named, ref, service, host, image, inlineKeys, pubsubEndpoint)
}

func gatewayService(t *testing.T, host string) *runv2.GoogleCloudRunV2Service {
	t.Helper()
	names := names.New(provider.Namespace("ocel"), "acme-prod")
	desired, err := run.ServiceOf(servingOf(t, names, googletest.ProductionRef(t), host, "europe-west1-docker.pkg.dev/acme-prod/ocel-acme-prod-production/ocel-realtime:abc", "", ""))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	return desired
}

func TestTheGatewayServiceTakesBrowsersFromAnywhereWithoutCloudRunsInvokerCheck(t *testing.T) {
	t.Parallel()

	desired := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app")
	if desired.Ingress != run.IngressEverywhere {
		t.Errorf("Ingress = %q, want %q: browsers connect straight to the gateway", desired.Ingress, run.IngressEverywhere)
	}
	if !desired.InvokerIamDisabled {
		t.Error("InvokerIamDisabled = false, and a browser carries no Google identity to pass Cloud Run's invoker check")
	}
}

func TestTheGatewayRunsUpToTenInstancesOfOneVCPUAndAGibibyteEachHoldingAThousandSocketsForAnHour(t *testing.T) {
	t.Parallel()

	template := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app").Template
	if template.Scaling.MinInstanceCount != 0 || template.Scaling.MaxInstanceCount != 10 {
		t.Errorf("instances = %d to %d, want 0 to 10: every instance hears every publish through its own subscription",
			template.Scaling.MinInstanceCount, template.Scaling.MaxInstanceCount)
	}
	if template.MaxInstanceRequestConcurrency != 1000 {
		t.Errorf("concurrency = %d, want 1000", template.MaxInstanceRequestConcurrency)
	}
	if template.Timeout != "3600s" {
		t.Errorf("timeout = %q, want 3600s, the longest Cloud Run holds a socket", template.Timeout)
	}
	resources := template.Containers[0].Resources
	if !maps.Equal(resources.Limits, map[string]string{"cpu": "1", "memory": "1024Mi"}) {
		t.Errorf("limits = %v, want 1 vCPU and 1Gi", resources.Limits)
	}
	if !resources.CpuIdle {
		t.Error("CpuIdle = false, and the gateway is billed per request, which an open socket keeps active")
	}
}

func TestTheGatewayReadsItsKeysFromTheLatestVersionOfItsEnvironmentsKeysSecret(t *testing.T) {
	t.Parallel()

	desired := gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app")
	names := names.New(provider.Namespace("ocel"), "acme-prod")
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
	env := googletest.EnvOf(desired.Template.Containers[0])
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

	env := googletest.EnvOf(gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app").Template.Containers[0])
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

func TestTheGatewayHearsItsSiblingInstancesThroughItsEnvironmentsRealtimeTopic(t *testing.T) {
	t.Parallel()

	env := googletest.EnvOf(gatewayService(t, "ocel-shop-prod-realtime-abc123-ew.a.run.app").Template.Containers[0])
	names := names.New(provider.Namespace("ocel"), "acme-prod")
	gateway, err := names.RealtimeGateway("shop", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if want := "projects/acme-prod/topics/" + gateway; env[pubsubbus.TopicVar] != want {
		t.Errorf("%s = %q, want %q", pubsubbus.TopicVar, env[pubsubbus.TopicVar], want)
	}
	if endpoint, set := env[pubsubbus.EndpointVar]; set {
		t.Errorf("%s = %q, want it unset so the gateway reaches Google's Pub/Sub", pubsubbus.EndpointVar, endpoint)
	}
}

func TestOnTheEmulatorTheGatewayReachesTheEmulatorsPubSub(t *testing.T) {
	t.Parallel()

	names := names.New(provider.Namespace("ocel"), "acme-prod")
	desired, err := run.ServiceOf(servingOf(t, names, googletest.ProductionRef(t), "gateway.run.app", "ocel-realtime:abc", `{"app":"AAAA"}`, "http://host.docker.internal:4588"))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	if got := googletest.EnvOf(desired.Template.Containers[0])[pubsubbus.EndpointVar]; got != "http://host.docker.internal:4588" {
		t.Errorf("%s = %q, want the emulator as a container reaches it", pubsubbus.EndpointVar, got)
	}
}

func TestOnTheEmulatorTheGatewayTakesItsKeysFromItsEnvironmentForWantOfSecretVolumes(t *testing.T) {
	t.Parallel()

	names := names.New(provider.Namespace("ocel"), "acme-prod")
	keys := `{"app":"AAAA"}`
	desired, err := run.ServiceOf(servingOf(t, names, googletest.ProductionRef(t), "gateway.run.app", "ocel-realtime:abc", keys, "http://host.docker.internal:4588"))
	if err != nil {
		t.Fatalf("serviceOf() = %v", err)
	}
	if len(desired.Template.Volumes) != 0 {
		t.Errorf("volumes = %d, want none: the emulator runs no secret volume", len(desired.Template.Volumes))
	}
	env := googletest.EnvOf(desired.Template.Containers[0])
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
	if h.run.Serving() != nil {
		t.Error("a gateway was released to run as an account the tier does not have")
	}
}

func TestAGatewayCloudRunServesOnAnotherHostEachReleaseIsRefused(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.run.UriEachRevision = true
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
	h.run.Mu.Lock()
	h.run.Service.Template.Containers[0].Image += "-older"
	h.run.Mu.Unlock()
	h.provision(t, "app")

	service := h.run.Serving()
	if served, latest := run.ServedRevision(service), run.RevisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the gateway serves %q, want %q, the revision its release made: an upgraded gateway otherwise never takes a socket", served, latest)
	}
}

func TestAGatewayWhoseLatestRevisionServesNoTrafficIsRoutedToItOnTheNextDeploy(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.run.Mu.Lock()
	h.run.Service.Traffic = []*runv2.GoogleCloudRunV2TrafficTarget{{Type: run.TrafficByRevision, Revision: "an-older-revision", Percent: 100}}
	h.run.Mu.Unlock()
	h.provision(t, "app")

	service := h.run.Serving()
	if served, latest := run.ServedRevision(service), run.RevisionName(service.LatestReadyRevision); served != latest {
		t.Errorf("the gateway serves %q, want %q: a release that came ready and was never routed is routed once the gateway is next deployed", served, latest)
	}
}

func TestARealtimeGatewayIsLabelledWithNoApp(t *testing.T) {
	service := gatewayService(t, "gateway.run.app")

	if got, labelled := service.Labels[names.AppLabel]; labelled {
		t.Errorf("the gateway is labelled %s=%q, want no app label", names.AppLabel, got)
	}
}

func TestARealtimeGatewayIsLabelledWithTheProjectItServes(t *testing.T) {
	service := gatewayService(t, "gateway.run.app")

	if got := service.Labels[names.ProjectLabel]; got != "shop" {
		t.Errorf("the gateway of project shop is labelled %s=%q, want shop", names.ProjectLabel, got)
	}
}
