package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const behindTheWorkerImage = "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc"

type shieldingFront struct{ edge.Edge }

func (f shieldingFront) Facts() edge.Facts {
	facts := f.Edge.Facts()
	facts.ShieldsOrigin = true
	return facts
}

func adoptBehindTheWorker(t *testing.T, p *Provider, kinds ...edge.Kind) {
	t.Helper()
	p.records = fake.NewKeyValues()
	for _, kind := range append([]edge.Kind{cloudflareKind}, kinds...) {
		err := cloudflareorigin.AdoptOffers(context.Background(), p.resolved, p.records, environment.TierProduction, kind,
			fullOffers("c1"), &fake.Log{})
		if err != nil {
			t.Fatal(err)
		}
		mintOriginSecret(t, p, environment.TierProduction, kind)
	}
}

func mintOriginSecret(t *testing.T, p *Provider, tier environment.Tier, kind edge.Kind) {
	t.Helper()
	if err := cloudflareorigin.EnsureOriginSecret(context.Background(), p.resolved, tier, kind, time.Now(), &fake.Log{}); err != nil {
		t.Fatal(err)
	}
}

func behindTheWorker(spec provider.StackSpec) provider.StackSpec {
	spec.Edge = codeRunningFront{kind: cloudflareKind, runsCode: true, shields: true}
	return spec
}

func containerRelease(buildID string) provider.StackSpec {
	spec := functionRelease(buildID, behindTheWorkerImage)
	spec.App.Compute = provider.ComputeContainer
	spec.App.Functions = nil
	spec.App.Image = behindTheWorkerImage
	spec.App.HealthCheckPath = "/"
	return spec
}

func originSecretOf(t *testing.T, p *Provider) originsecret.Secret {
	t.Helper()
	secret, found, err := cloudflareorigin.ReadOriginSecret(context.Background(), p.resolved, environment.TierProduction, cloudflareKind)
	if err != nil || !found {
		t.Fatalf("ReadOriginSecret() = %+v, %v, %v, want the secret the bootstrap minted", secret, found, err)
	}
	return secret
}

func rotateOriginSecret(t *testing.T, p *Provider) originsecret.Secret {
	t.Helper()
	minted := originSecretOf(t, p)
	if err := cloudflareorigin.EnsureOriginSecret(context.Background(), p.resolved, environment.TierProduction, cloudflareKind, minted.CreatedAt.Add(originsecret.MaxAge), &fake.Log{}); err != nil {
		t.Fatal(err)
	}
	rotated := originSecretOf(t, p)
	if !rotated.Rotating() {
		t.Fatalf("stored %+v after a stale bootstrap, want a rotation", rotated)
	}
	return rotated
}

func releasedServices(server *googletest.Run) []*runv2.GoogleCloudRunV2Service {
	return slices.Concat(server.Created, server.Patched)
}

func releasedWith(server *googletest.Run, worker bool) *runv2.GoogleCloudRunV2Service {
	for _, service := range releasedServices(server) {
		if service.Template == nil {
			continue
		}
		if _, isWorker := googletest.EnvOf(service.Template.Containers[0])[processenv.WorkerEnvVar]; isWorker == worker {
			return service
		}
	}
	return nil
}

func tagged(service *runv2.GoogleCloudRunV2Service, tag string) *runv2.GoogleCloudRunV2TrafficTarget {
	for _, target := range service.Traffic {
		if target.Tag == tag {
			return target
		}
	}
	return nil
}

func TestAContainerBehindTheWorkerAnswersEveryCallerOnItsTagURLAndDemandsTheOriginSecret(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)
	secret := originSecretOf(t, p)
	spec := behindTheWorker(containerRelease("d1"))

	containers, err := p.ProvisionContainers(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers = %v", err)
	}

	service := server.Created[0]
	if service.Ingress != run.IngressEverywhere {
		t.Errorf("ingress = %s, want %s: the worker reaches the revision-tag URL directly, and the runtime refuses whoever lacks the origin secret", service.Ingress, run.IngressEverywhere)
	}
	if !service.InvokerIamDisabled || service.Labels[run.OpensOnPromotionLabel] != "" {
		t.Errorf("service opens %v with label %q, want it open at creation: the worker reaches it before any promotion, and IAM would answer it 403",
			service.InvokerIamDisabled, service.Labels[run.OpensOnPromotionLabel])
	}
	env := googletest.EnvOf(service.Template.Containers[0])
	if env[edge.OriginFrontedVar] != "1" || env[edge.OriginSecretVar] != secret.Current {
		t.Errorf("%s = %q and the secret set is %v, want the service fronted and handed the tier's current origin secret",
			edge.OriginFrontedVar, env[edge.OriginFrontedVar], env[edge.OriginSecretVar] == secret.Current)
	}
	if previous, set := env[edge.OriginSecretPreviousVar]; set {
		t.Errorf("%s set to %d characters with no rotation under way, want none", edge.OriginSecretPreviousVar, len(previous))
	}

	tag := spec.Ref.Name.Release.String()
	target := tagged(server.Service, tag)
	if target == nil || run.RevisionName(target.Revision) != containers[0].Revision {
		t.Fatalf("traffic = %+v, want the release's revision %s tagged %s", server.Service.Traffic, containers[0].Revision, tag)
	}
	if want := "https://" + tag + "---" + strings.TrimPrefix(server.Service.Uri, "https://"); containers[0].URL != want {
		t.Errorf("URL = %q, want the revision-tag URL %q the worker forwards to", containers[0].URL, want)
	}
}

func TestAServiceBehindTheWorkerAcceptsTheReplacedSecretWhileTheTierRotates(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)
	rotated := rotateOriginSecret(t, p)

	if _, err := p.ProvisionContainers(context.Background(), behindTheWorker(containerRelease("d1")), nil); err != nil {
		t.Fatalf("ProvisionContainers = %v", err)
	}

	env := googletest.EnvOf(server.Created[0].Template.Containers[0])
	if env[edge.OriginSecretVar] != rotated.Current || env[edge.OriginSecretPreviousVar] != rotated.Previous {
		t.Error("the service holds other than the tier's current and replaced origin secrets: a worker programmed before the rotation still presents the replaced one")
	}
}

func TestAContainerBehindTheWorkerIsProbedWithATokenItKeepsAcrossDeploys(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)

	if _, err := p.ProvisionContainers(context.Background(), behindTheWorker(containerRelease("d1")), nil); err != nil {
		t.Fatalf("ProvisionContainers = %v", err)
	}

	container := server.Service.Template.Containers[0]
	token := googletest.EnvOf(container)[originguard.ProbeTokenVar]
	if len(token) < 32 {
		t.Fatalf("%s = %q, want a token no caller can guess: the runtime answers its health path without the origin secret only to a request carrying it", originguard.ProbeTokenVar, token)
	}
	probe := container.StartupProbe
	if probe == nil || probe.HttpGet == nil || len(probe.HttpGet.HttpHeaders) != 1 ||
		probe.HttpGet.HttpHeaders[0].Name != originguard.ProbeTokenHeader || probe.HttpGet.HttpHeaders[0].Value != token {
		t.Errorf("startup probe = %+v, want it to send %s with the token the front holds", probe, originguard.ProbeTokenHeader)
	}

	if _, err := p.ProvisionContainers(context.Background(), behindTheWorker(containerRelease("d2")), nil); err != nil {
		t.Fatalf("ProvisionContainers = %v", err)
	}
	if got := googletest.EnvOf(server.Service.Template.Containers[0])[originguard.ProbeTokenVar]; got != token {
		t.Errorf("the second deploy set %s = %q, want %q: a token that changes each deploy rolls a revision for nothing", originguard.ProbeTokenVar, got, token)
	}
}

func TestAWorkerServiceOfAnAppBehindTheWorkerIsHandedNoOriginSecret(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)
	spec := behindTheWorker(containerRelease("d1"))
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers = %v", err)
	}

	worker := releasedWith(server, true)
	if worker == nil {
		t.Fatal("no worker service was released")
	}
	env := googletest.EnvOf(worker.Template.Containers[0])
	for _, name := range []string{edge.OriginFrontedVar, edge.OriginSecretVar, edge.OriginSecretPreviousVar} {
		if _, set := env[name]; set {
			t.Errorf("the worker service sets %s, want none: only Pub/Sub reaches a worker, and it presents no origin secret", name)
		}
	}
	if app := releasedWith(server, false); app == nil || googletest.EnvOf(app.Template.Containers[0])[edge.OriginSecretVar] == "" {
		t.Error("the app's own service holds no origin secret beside its worker")
	}
}

func TestAContainerBehindTheWorkerOnATierWithNoOriginSecretIsRefusedBeforeAnythingRuns(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.records = fake.NewKeyValues()
	if err := cloudflareorigin.AdoptOffers(context.Background(), p.resolved, p.records, environment.TierProduction, cloudflareKind, fullOffers("c1"), &fake.Log{}); err != nil {
		t.Fatal(err)
	}

	_, err := p.ProvisionContainers(context.Background(), behindTheWorker(containerRelease("d1")), nil)

	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), "origin secret") || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Errorf("ProvisionContainers = %v, want a not-ready refusal naming the origin secret and the bootstrap", err)
	}
	if len(server.Created) != 0 {
		t.Errorf("created %d services before refusing, want none", len(server.Created))
	}
}

func TestAFunctionBehindTheWorkerAnswersEveryCallerOnItsTagURLAndDemandsTheOriginSecret(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)
	secret := originSecretOf(t, p)
	spec := behindTheWorker(functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout:sha256-one"))

	functions, err := p.ProvisionFunctions(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionFunctions = %v", err)
	}

	service := server.Created[0]
	if service.Ingress != run.IngressEverywhere || !service.InvokerIamDisabled {
		t.Errorf("ingress = %s and open = %v, want every caller let through to the runtime, which demands the origin secret", service.Ingress, service.InvokerIamDisabled)
	}
	env := googletest.EnvOf(service.Template.Containers[0])
	if env[edge.OriginFrontedVar] != "1" || env[edge.OriginSecretVar] != secret.Current {
		t.Error("the function is not fronted or not handed the tier's current origin secret")
	}
	tag := spec.Ref.Name.Release.String()
	if want := "https://" + tag + "---" + strings.TrimPrefix(server.Service.Uri, "https://"); functions[0].URL != want {
		t.Errorf("URL = %q, want the revision-tag URL %q", functions[0].URL, want)
	}
}

func TestAServiceBehindTheLoadBalancerIsHandedNoOriginSecretAndKeepsItsIngress(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p)
	spec := containerRelease("d1")
	spec.Edge = codeRunningFront{kind: "alb", shields: true}

	containers, err := p.ProvisionContainers(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}

	service := server.Created[0]
	if service.Ingress != run.IngressLoadBalancer || service.InvokerIamDisabled || service.Labels[run.OpensOnPromotionLabel] == "" {
		t.Errorf("ingress = %s, open = %v, label %q: want the load balancer alone, shut until its promotion", service.Ingress, service.InvokerIamDisabled, service.Labels[run.OpensOnPromotionLabel])
	}
	env := googletest.EnvOf(service.Template.Containers[0])
	for _, name := range []string{edge.OriginFrontedVar, edge.OriginSecretVar} {
		if _, set := env[name]; set {
			t.Errorf("a service behind the load balancer sets %s, want none", name)
		}
	}
	if containers[0].URL != server.Service.Uri {
		t.Errorf("URL = %q, want the service's own %q", containers[0].URL, server.Service.Uri)
	}
}
