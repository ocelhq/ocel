package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var shopGate = gate.Gate{
	Key:           "shop-key",
	PasswordMAC:   gate.HashPassword("shop-key", "correct horse battery"),
	BypassSecrets: []string{"shop-bypass"},
	AllowOptions:  []string{"/api/"},
}

func publishGate(t *testing.T, p *Provider, held *gate.Gate) {
	t.Helper()
	publishGateOn(t, p, environment.TierPreview, held)
}

func publishGateOn(t *testing.T, p *Provider, tier environment.Tier, held *gate.Gate) {
	t.Helper()
	if err := (gateSecrets{p: p}).Publish(context.Background(), tier, "shop", held); err != nil {
		t.Fatal(err)
	}
}

func openPreviewSpec(p *Provider) provider.StackSpec {
	spec := previewSpec()
	spec.Edge = p.edges().openCloudRun()
	return spec
}

func gatedPreviewSpec(p *Provider, held *gate.Gate) provider.StackSpec {
	spec := openPreviewSpec(p)
	spec.App.Gate = held
	return spec
}

func openProductionSpec(p *Provider) provider.StackSpec {
	spec := openPreviewSpec(p)
	spec.Ref.Tier = environment.TierProduction
	spec.Ref.Name.Env = "prod"
	return spec
}

func gatedProductionSpec(p *Provider, held *gate.Gate) provider.StackSpec {
	spec := openProductionSpec(p)
	spec.App.Gate = held
	return spec
}

func functionSpec(spec provider.StackSpec, framework string) provider.StackSpec {
	spec.App.Compute = provider.ComputeServerless
	spec.App.Functions[0].Framework = buildoutput.Framework{Name: framework, Arch: "x86_64"}
	return spec
}

func TestAGatedPreviewContainerMountsTheProjectsGateSecretAtItsLatestVersion(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	secret := names(t, p).GateSecret(environment.TierPreview, "shop")
	volumes := server.service.Template.Volumes
	if len(volumes) != 1 || volumes[0].Secret == nil || volumes[0].Secret.Secret != secret ||
		len(volumes[0].Secret.Items) != 1 || volumes[0].Secret.Items[0].Version != "latest" {
		t.Fatalf("volumes = %+v, want the project's gate secret %s mounted at version latest: a new version then reaches every instance with no new revision", volumes, secret)
	}
	mounts := server.service.Template.Containers[0].VolumeMounts
	file := mounts[0].MountPath + "/" + volumes[0].Secret.Items[0].Path
	if got := envOf(server.service.Template.Containers[0])[gate.MountedFileVar]; got != file {
		t.Errorf("%s = %q, want the mounted file %s", gate.MountedFileVar, got, file)
	}
	if _, inline := envOf(server.service.Template.Containers[0])[gate.MountedInlineVar]; inline {
		t.Errorf("the service carries its gate in %s, want it only in the mounted secret", gate.MountedInlineVar)
	}
}

func TestAGatedPreviewContainersStartupProbeCarriesTheTokenItsFrontExemptsAlone(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	container := server.service.Template.Containers[0]
	token := envOf(container)[originguard.ProbeTokenVar]
	if len(token) < 32 {
		t.Fatalf("%s = %q, want a token no visitor can guess", originguard.ProbeTokenVar, token)
	}
	if strings.Contains(token, shopGate.Key) {
		t.Errorf("%s = %q holds the gate's key", originguard.ProbeTokenVar, token)
	}
	probe := container.StartupProbe
	if probe == nil || probe.HttpGet == nil || len(probe.HttpGet.HttpHeaders) != 1 ||
		probe.HttpGet.HttpHeaders[0].Name != originguard.ProbeTokenHeader || probe.HttpGet.HttpHeaders[0].Value != token {
		t.Errorf("startup probe = %+v, want it to send %s with the token the front holds", probe, originguard.ProbeTokenHeader)
	}
}

func TestAPreviewContainerKeepsItsProbeTokenAcrossDeploysAndGateRotations(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)
	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	token := envOf(server.service.Template.Containers[0])[originguard.ProbeTokenVar]
	rotated := shopGate
	rotated.Key = "rotated-key"
	publishGate(t, p, &rotated)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &rotated), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if got := envOf(server.service.Template.Containers[0])[originguard.ProbeTokenVar]; got != token {
		t.Errorf("the second deploy set %s = %q, want %q: a token that changes each deploy rolls a revision for nothing", originguard.ProbeTokenVar, got, token)
	}
}

func TestAnOpenPreviewContainerOnCloudRunProbesWithATokenSoTurningProtectionOnLeavesNewInstancesStarting(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)

	if _, err := p.ProvisionContainers(context.Background(), openPreviewSpec(p), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	container := server.service.Template.Containers[0]
	token := envOf(container)[originguard.ProbeTokenVar]
	probe := container.StartupProbe.HttpGet
	if len(token) < 32 || len(probe.HttpHeaders) != 1 || probe.HttpHeaders[0].Value != token {
		t.Errorf("an open preview's probe sends %+v and its front holds %q, want the same token: once a gate is published, a probe without it gets the login page and no new instance starts", probe.HttpHeaders, token)
	}
}

func TestAGatedProductionContainerMountsTheProductionGateSecretAndNeverThePreviews(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	production := shopGate
	production.Key = "production-key"
	publishGate(t, p, &shopGate)
	publishGateOn(t, p, environment.TierProduction, &production)

	if _, err := p.ProvisionContainers(context.Background(), gatedProductionSpec(p, &production), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	secret := names(t, p).GateSecret(environment.TierProduction, "shop")
	volumes := server.service.Template.Volumes
	if len(volumes) != 1 || volumes[0].Secret == nil || volumes[0].Secret.Secret != secret || volumes[0].Secret.Items[0].Version != "latest" {
		t.Fatalf("volumes = %+v, want the production gate secret %s mounted at latest", volumes, secret)
	}
	container := server.service.Template.Containers[0]
	if token := envOf(container)[originguard.ProbeTokenVar]; len(token) < 32 || len(container.StartupProbe.HttpGet.HttpHeaders) != 1 {
		t.Errorf("a gated production container probes with %+v and holds %q, want the token its front exempts", container.StartupProbe.HttpGet.HttpHeaders, token)
	}
}

func TestAnOpenProductionOnCloudRunMountsItsGateSecretSoTurningProtectionOnReachesItWithNoNewRevision(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGateOn(t, p, environment.TierProduction, nil)

	if _, err := p.ProvisionContainers(context.Background(), openProductionSpec(p), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	revisions := server.revision
	publishGateOn(t, p, environment.TierProduction, &shopGate)

	secret := names(t, p).GateSecret(environment.TierProduction, "shop")
	if volumes := server.service.Template.Volumes; len(volumes) != 1 || volumes[0].Secret.Secret != secret {
		t.Fatalf("volumes = %+v, want the production gate secret %s mounted while protection is off", volumes, secret)
	}
	if server.revision != revisions {
		t.Errorf("turning protection on created %d revisions, want none", server.revision-revisions)
	}
}

func TestEachTiersServiceMayReadOnlyItsOwnTiersGateSecret(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	production := shopGate
	production.Key = "production-key"
	publishGate(t, p, &shopGate)
	publishGateOn(t, p, environment.TierProduction, &production)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers(preview) = %v", err)
	}
	if _, err := p.ProvisionContainers(context.Background(), gatedProductionSpec(p, &production), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers(production) = %v", err)
	}

	previewAccount := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierPreview, "shop", "pr-7", "web")
	productionAccount := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web")
	productionReaders := server.secrets.members(names(t, p).GateSecret(environment.TierProduction, "shop"), gateReaderRole)
	previewReaders := server.secrets.members(names(t, p).GateSecret(environment.TierPreview, "shop"), gateReaderRole)
	if !slices.Contains(productionReaders, productionAccount) || slices.Contains(productionReaders, previewAccount) {
		t.Errorf("the production gate secret lets %v read it, want %s and never the preview account %s", productionReaders, productionAccount, previewAccount)
	}
	if !slices.Contains(previewReaders, previewAccount) || slices.Contains(previewReaders, productionAccount) {
		t.Errorf("the preview gate secret lets %v read it, want %s alone", previewReaders, previewAccount)
	}
}

func TestRevokingAGatedProductionAppTakesItsReadOfTheProductionGateSecretAway(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGateOn(t, p, environment.TierProduction, &shopGate)
	spec := gatedProductionSpec(p, &shopGate)
	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	c, err := p.openClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if err := p.revokeAfterRemoval(context.Background(), c, spec.Ref, nil, nil, nil); err != nil {
		t.Fatalf("revokeAfterRemoval() = %v", err)
	}

	member := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web")
	if readers := server.secrets.members(names(t, p).GateSecret(environment.TierProduction, "shop"), gateReaderRole); slices.Contains(readers, member) {
		t.Errorf("the production gate secret still lets %s read it after no environment runs the app", member)
	}
}

func TestAGatedProductionFunctionOfAFrameworkThatCannotEnforceTheGateIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := functionSpec(gatedProductionSpec(p, &shopGate), buildoutput.FrameworkPython)

	_, err := p.ProvisionFunctions(context.Background(), spec, &fake.Log{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		t.Fatalf("ProvisionFunctions() = %v, want an unsupported refusal: the python function would serve a protected production to anyone", err)
	}
	if !strings.Contains(refused.Message, "protection.production") {
		t.Errorf("the refusal reads %q, want it to name protection.production", refused.Message)
	}
	if server.service != nil {
		t.Error("the refused deploy still created the service")
	}
}

func TestProvisioningAGatedPreviewLeavesTheGateTheEdgeLastPublishedInItsSecret(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	rotated := shopGate
	rotated.Key = "rotated-key"
	rotated.PasswordMAC = gate.HashPassword("rotated-key", "a password set after the deploy read the gate")
	publishGate(t, p, &rotated)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	held, _ := server.secrets.latest(t, names(t, p).GateSecret(environment.TierPreview, "shop"))
	if decoded, err := gate.DecodeMounted([]byte(held)); err != nil || decoded == nil || decoded.Key != rotated.Key {
		t.Errorf("the secret holds %q (%v), want the gate published after the deploy read its own: provisioning wrote back credentials a rotation had replaced", held, err)
	}
}

func TestAGatedPreviewWhoseSecretHoldsNoGateIsNotDeployed(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)

	_, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{})

	if err == nil {
		t.Fatal("ProvisionContainers() = nil, want an error: the service would mount a secret that gates nothing")
	}
	if server.service != nil {
		t.Error("the service was deployed with no gate in the secret it mounts")
	}
}

func TestAPreviewWhoseGateSecretIsMissingIsNotDeployed(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	_, err := p.ProvisionContainers(context.Background(), openPreviewSpec(p), &fake.Log{})

	if err == nil {
		t.Fatal("ProvisionContainers() = nil, want an error: the service would mount a secret that does not exist and answer every request 503")
	}
	if server.service != nil {
		t.Error("the service was deployed mounting a secret that does not exist")
	}
}

func TestAnOpenPreviewOnCloudRunMountsTheGateSecretSoTurningProtectionOnReachesItWithNoNewRevision(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)

	if _, err := p.ProvisionContainers(context.Background(), openPreviewSpec(p), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	revisions := server.revision
	publishGate(t, p, &shopGate)

	secret := names(t, p).GateSecret(environment.TierPreview, "shop")
	if volumes := server.service.Template.Volumes; len(volumes) != 1 || volumes[0].Secret.Secret != secret || volumes[0].Secret.Items[0].Version != "latest" {
		t.Fatalf("volumes = %+v, want the project's gate secret %s mounted at latest while protection is off", volumes, secret)
	}
	member := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierPreview, "shop", "pr-7", "web")
	if readers := server.secrets.members(secret, gateReaderRole); !slices.Contains(readers, member) {
		t.Errorf("the gate secret lets %v read it, want the app's account %s before the service mounts it", readers, member)
	}
	if server.revision != revisions {
		t.Errorf("turning protection on created %d revisions, want none", server.revision-revisions)
	}
	held, _ := server.secrets.latest(t, secret)
	if decoded, err := gate.DecodeMounted([]byte(held)); err != nil || decoded == nil || decoded.Key != shopGate.Key {
		t.Errorf("the mounted secret holds %q (%v), want the published gate", held, err)
	}
}

func TestAnOpenPreviewFunctionThatCannotCheckTheGateDeploysUnmountedAndIsRecordedAsIgnoringIt(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)

	functions, err := p.ProvisionFunctions(context.Background(), functionSpec(openPreviewSpec(p), buildoutput.FrameworkPython), &fake.Log{})
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v, want an open preview to deploy as it always has", err)
	}

	if volumes := server.service.Template.Volumes; len(volumes) != 0 {
		t.Errorf("volumes = %+v, want none: nothing in a python function reads the gate", volumes)
	}
	if len(functions) != 1 || !functions[0].IgnoresGate {
		t.Errorf("ProvisionFunctions() = %+v, want the function recorded as ignoring the gate, so turning protection on later cannot call it protected", functions)
	}
}

func TestAnOpenPreviewFunctionThatChecksTheGateMountsItAndIsNotRecordedAsIgnoringIt(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)

	functions, err := p.ProvisionFunctions(context.Background(), functionSpec(openPreviewSpec(p), buildoutput.FrameworkNode), &fake.Log{})
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if volumes := server.service.Template.Volumes; len(volumes) != 1 {
		t.Errorf("volumes = %+v, want the project's gate secret", volumes)
	}
	if len(functions) != 1 || functions[0].IgnoresGate {
		t.Errorf("ProvisionFunctions() = %+v, want the function not recorded as ignoring the gate", functions)
	}
}

func TestAGatedPreviewFunctionMountsTheGateToo(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)
	spec := functionSpec(gatedPreviewSpec(p, &shopGate), buildoutput.FrameworkNode)

	if _, err := p.ProvisionFunctions(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if volumes := server.service.Template.Volumes; len(volumes) != 1 || volumes[0].Secret.Secret != names(t, p).GateSecret(environment.TierPreview, "shop") {
		t.Errorf("volumes = %+v, want the project's gate secret mounted", volumes)
	}
	if got := envOf(server.service.Template.Containers[0])[gate.MountedFileVar]; got == "" {
		t.Errorf("the function names no %s, and its runtime reads the gate from there", gate.MountedFileVar)
	}
}

func TestAGatedPreviewIsNotSaidToBeOpenToAnyoneWithItsUrl(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)
	progress := &fake.Log{}

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), progress); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if slices.ContainsFunc(progress.Lines(), func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("the release said %q, and a service that gates its own preview does not answer anyone with its url", progress.Lines())
	}
}

func TestAPreviewBehindAnEdgeThatGatesItselfMountsNothingAndCreatesNoSecret(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec()
	spec.Edge = codeRunningFront{kind: cloudflareKind, runsCode: true, shields: true}

	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if len(server.service.Template.Volumes) != 0 || envOf(server.service.Template.Containers[0])[gate.MountedFileVar] != "" {
		t.Errorf("the preview mounts %+v and names %q, and its edge runs the gate", server.service.Template.Volumes, envOf(server.service.Template.Containers[0])[gate.MountedFileVar])
	}
	if server.secrets != nil && server.secrets.has(names(t, p).GateSecret(environment.TierPreview, "shop")) {
		t.Error("a preview whose edge gates it created a gate secret")
	}
}

func TestAGatedPreviewServiceMayReadOnlyItsProjectsGateSecret(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	member := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierPreview, "shop", "pr-7", "web")
	secret := names(t, p).GateSecret(environment.TierPreview, "shop")
	if readers := server.secrets.members(secret, gateReaderRole); !slices.Contains(readers, member) {
		t.Errorf("the gate secret %s lets %v read it, want the app's account %s", secret, readers, member)
	}
	for _, bound := range projectBindingsOf(server.identities()) {
		if strings.HasPrefix(bound, gateReaderRole+" ") {
			t.Errorf("the project binds %q, want no project-wide secret reader: the gate is granted on its own secret", bound)
		}
	}
}

func TestRevokingAGatedPreviewAppTakesItsReadOfTheGateSecretAway(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)
	spec := gatedPreviewSpec(p, &shopGate)
	if _, err := p.ProvisionContainers(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	c, err := p.openClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if err := p.revokeAfterRemoval(context.Background(), c, spec.Ref, nil, nil, nil); err != nil {
		t.Fatalf("revokeAfterRemoval() = %v", err)
	}

	member := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierPreview, "shop", "pr-7", "web")
	if readers := server.secrets.members(names(t, p).GateSecret(environment.TierPreview, "shop"), gateReaderRole); slices.Contains(readers, member) {
		t.Errorf("the gate secret still lets %s read it after no environment runs the app", member)
	}
}

func TestAGatedPreviewFunctionOfAFrameworkThatCannotEnforceTheGateIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := functionSpec(gatedPreviewSpec(p, &shopGate), buildoutput.FrameworkPython)

	_, err := p.ProvisionFunctions(context.Background(), spec, &fake.Log{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		t.Fatalf("ProvisionFunctions() = %v, want an unsupported refusal: the python function would serve a protected preview to anyone", err)
	}
	if server.service != nil {
		t.Error("the refused deploy still created the service")
	}
}

func TestAnEmulatedGatedPreviewCarriesItsGateInItsEnvironmentBecauseTheEmulatorMountsNoSecrets(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, &shopGate)
	p.gateInEnv = true

	if _, err := p.ProvisionContainers(context.Background(), gatedPreviewSpec(p, &shopGate), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if len(server.service.Template.Volumes) != 0 {
		t.Errorf("volumes = %+v, want none: floci-gcp mounts no secret volume", server.service.Template.Volumes)
	}
	var got gate.Gate
	if err := json.Unmarshal([]byte(envOf(server.service.Template.Containers[0])[gate.MountedInlineVar]), &got); err != nil || got.Key != shopGate.Key {
		t.Errorf("%s = %q (%v), want the project's gate", gate.MountedInlineVar, envOf(server.service.Template.Containers[0])[gate.MountedInlineVar], err)
	}
}

func TestAnEmulatedOpenPreviewCarriesTheWithdrawnMarkerInItsEnvironment(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	publishGate(t, p, nil)
	p.gateInEnv = true

	if _, err := p.ProvisionContainers(context.Background(), openPreviewSpec(p), &fake.Log{}); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	if got := envOf(server.service.Template.Containers[0])[gate.MountedInlineVar]; got != "null" {
		t.Errorf("%s = %q, want the withdrawn marker the runtime reads as an open preview", gate.MountedInlineVar, got)
	}
}

func openGateSecrets(t *testing.T) (*runServer, *clients) {
	t.Helper()
	server := &runServer{}
	p := server.open(t)
	c, err := p.openClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return server, c
}

func TestPublishingAGateAddsAVersionTheMountedFileReadsWithNoNewRevision(t *testing.T) {
	server, c := openGateSecrets(t)
	rotated := shopGate
	rotated.Key = "rotated-key"

	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}
	before := server.secrets.versionsAdded()
	revisions := server.revision
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &rotated); err != nil {
		t.Fatal(err)
	}

	if got := server.secrets.versionsAdded(); got != before+1 {
		t.Errorf("a rotated gate added %d versions, want 1", got-before)
	}
	if server.revision != revisions {
		t.Errorf("publishing created %d revisions, want none: the mounted file is read live", server.revision-revisions)
	}
}

func TestPublishingAGateKeepsTheVersionItReplacesReadableAndDestroysOlderOnes(t *testing.T) {
	server, c := openGateSecrets(t)
	gates := []gate.Gate{shopGate, shopGate, shopGate}
	gates[1].Key, gates[2].Key = "second-key", "third-key"

	for i := range gates {
		if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &gates[i]); err != nil {
			t.Fatal(err)
		}
	}

	if got := server.secrets.enabledVersions(c.GateSecret(environment.TierPreview, "shop")); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("the secret keeps versions %v readable, want 2 and 3: an instance that resolved latest just before the publish still reads the version it resolved", got)
	}
}

func TestPublishingTheGateASecretAlreadyHoldsAddsNoVersion(t *testing.T) {
	server, c := openGateSecrets(t)
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}
	before := server.secrets.versionsAdded()

	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}

	if got := server.secrets.versionsAdded(); got != before {
		t.Errorf("an unchanged gate added %d versions, want none", got-before)
	}
}

func TestWithdrawingAGateOpensTheServicesThatMountIt(t *testing.T) {
	server, c := openGateSecrets(t)
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", nil); err != nil {
		t.Fatal(err)
	}

	held, _ := server.secrets.latest(t, c.GateSecret(environment.TierPreview, "shop"))
	if decoded, err := gate.DecodeMounted([]byte(held)); err != nil || decoded != nil {
		t.Errorf("the secret holds %q, want the withdrawn marker the runtimes read as an open preview", held)
	}
}

func TestWithdrawingTheGateOfAProjectThatNeverHadOneCreatesItsSecretHoldingTheWithdrawnMarker(t *testing.T) {
	server, c := openGateSecrets(t)

	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", nil); err != nil {
		t.Fatal(err)
	}

	if held, _ := server.secrets.latest(t, c.GateSecret(environment.TierPreview, "shop")); held != "null" {
		t.Errorf("the secret holds %q, want the withdrawn marker: an open preview's services mount it before any gate exists", held)
	}
}

func TestRemovingAProjectsGateDeletesItsSecret(t *testing.T) {
	server, c := openGateSecrets(t)
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}

	if err := c.deleteGate(t.Context(), environment.TierPreview, "shop"); err != nil {
		t.Fatal(err)
	}

	if server.secrets.has(c.GateSecret(environment.TierPreview, "shop")) {
		t.Error("the gate secret outlived the project's previews")
	}
	if err := c.deleteGate(t.Context(), environment.TierPreview, "shop"); err != nil {
		t.Errorf("deleting a gate secret that is gone = %v, want it idempotent", err)
	}
}

func TestTwoProjectsGateSecretsAreNamedApartAndUnderTheGrantedPrefix(t *testing.T) {
	_, c := openGateSecrets(t)

	if c.GateSecret(environment.TierPreview, "shop") == c.GateSecret(environment.TierPreview, "blog") {
		t.Error("two projects share a gate secret")
	}
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		for _, slug := range []string{"shop", strings.Repeat("long-", 80)} {
			name := c.GateSecret(tier, slug)
			if !strings.HasPrefix(name, c.GateTierSecretPrefix(tier)) || !strings.HasPrefix(name, c.GateSecretPrefix()) || len(name) > 255 {
				t.Errorf("GateSecret(%s, %q) = %q (%d bytes), want a Secret Manager id under %s", tier, slug, name, len(name), c.GateTierSecretPrefix(tier))
			}
		}
	}
}

func TestAProjectsProductionAndPreviewGateSecretsAreNamedApartUnderTheirTiersPrefixes(t *testing.T) {
	_, c := openGateSecrets(t)

	production, preview := c.GateSecret(environment.TierProduction, "shop"), c.GateSecret(environment.TierPreview, "shop")
	if production == preview {
		t.Fatal("production and preview share a gate secret, and a preview password would open production")
	}
	if strings.HasPrefix(preview, c.GateTierSecretPrefix(environment.TierProduction)) || strings.HasPrefix(production, c.GateTierSecretPrefix(environment.TierPreview)) {
		t.Errorf("%s and %s fall under each other's tier prefix, so a grant scoped to one tier reaches the other's", production, preview)
	}
}

func TestPublishingAProductionGateLeavesThePreviewSecretAlone(t *testing.T) {
	server, c := openGateSecrets(t)
	production := shopGate
	production.Key = "production-key"
	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatal(err)
	}

	if err := c.writeGate(t.Context(), environment.TierProduction, "shop", &production); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteGate(t.Context(), environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}

	held, _ := server.secrets.latest(t, c.GateSecret(environment.TierPreview, "shop"))
	if decoded, err := gate.DecodeMounted([]byte(held)); err != nil || decoded == nil || decoded.Key != shopGate.Key {
		t.Errorf("the preview secret holds %q (%v), want the preview gate untouched by production's publish and removal", held, err)
	}
}

func TestAGateSecretIsAPublishedGateTheEdgeConformanceSuiteAccepts(t *testing.T) {
	_, c := openGateSecrets(t)

	edgeconformance.RunPublishedGate(t, edgeconformance.PublishedGate{
		Publish: func(ctx context.Context, held *gate.Gate) error {
			return c.writeGate(ctx, environment.TierPreview, "shop", held)
		},
		Read: func(ctx context.Context) (*gate.Gate, error) {
			held, _, err := c.readGate(ctx, environment.TierPreview, "shop")
			return held, err
		},
	})
}

func TestAGateSecretAnotherWriterCreatedFirstWithTheSameGateTakesNoSecondVersion(t *testing.T) {
	server, c := openGateSecrets(t)
	server.secrets = newSecretServer()
	server.secrets.writtenFirstElsewhere = map[string]string{c.GateSecret(environment.TierPreview, "shop"): "null"}

	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", nil); err != nil {
		t.Fatalf("writeGate() = %v, want a secret another writer created first to be written as one that already existed", err)
	}

	if got := server.secrets.versionsAdded(); got != 0 {
		t.Errorf("the write added %d versions over the one another writer had just added with the same gate, want none", got)
	}
}

func TestAGateSecretAnotherWriterCreatedFirstWithAnotherGateTakesTheNewOne(t *testing.T) {
	server, c := openGateSecrets(t)
	server.secrets = newSecretServer()
	server.secrets.writtenFirstElsewhere = map[string]string{c.GateSecret(environment.TierPreview, "shop"): "null"}

	if err := c.writeGate(t.Context(), environment.TierPreview, "shop", &shopGate); err != nil {
		t.Fatalf("writeGate() = %v", err)
	}

	held, _ := server.secrets.latest(t, c.GateSecret(environment.TierPreview, "shop"))
	if decoded, err := gate.DecodeMounted([]byte(held)); err != nil || decoded == nil || decoded.Key != shopGate.Key {
		t.Errorf("the secret holds %q (%v), want the gate this write was asked for", held, err)
	}
}

func pythonFunctionsSpec(p *Provider, names ...string) provider.StackSpec {
	spec := openPreviewSpec(p)
	spec.App.Compute = provider.ComputeServerless
	spec.App.Functions = nil
	for _, name := range names {
		spec.App.Functions = append(spec.App.Functions, provider.FunctionSpec{
			Name:      name,
			Image:     "europe-west1-docker.pkg.dev/acme/ocel/" + name + "@sha256:abc",
			Framework: buildoutput.Framework{Name: buildoutput.FrameworkPython, Arch: "x86_64"},
		})
	}
	return spec
}

func recordedFunctions(t *testing.T, p *Provider, spec provider.StackSpec) []provider.Function {
	t.Helper()
	recorded, _, err := stackrecords.Read(context.Background(), p.KeyValues(), spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name)
	if err != nil {
		t.Fatal(err)
	}
	return recorded.Functions
}

func TestAnOpenPreviewFunctionThatIgnoresTheGateIsRecordedAsIgnoringItBeforeItsServiceIsCreated(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := pythonFunctionsSpec(p, "fn--web--api")
	var recordedAtCreate []provider.Function
	server.onCreate = func() { recordedAtCreate = recordedFunctions(t, p, spec) }

	if _, err := p.Stacks().Provision(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if len(recordedAtCreate) != 1 || !recordedAtCreate[0].IgnoresGate {
		t.Errorf("when its service was created the stack recorded %+v, want the function recorded as ignoring the gate already: a publish that reads the records then would otherwise call the previews protected", recordedAtCreate)
	}
}

func TestAPreviewWhoseProvisioningFailsAfterItsGateIgnoringServiceIsLiveKeepsItRecordedAsIgnoringTheGate(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := pythonFunctionsSpec(p, "fn--web--api", "fn--web--jobs")
	spec.App.Functions[1].Image = ""

	if _, err := p.Stacks().Provision(context.Background(), spec, &fake.Log{}); err == nil {
		t.Fatal("Provision() succeeded, want the function that names no image refused")
	}

	if server.service == nil {
		t.Fatal("the first function's service was never created, and this test needs it live")
	}
	recorded := recordedFunctions(t, p, spec)
	at := slices.IndexFunc(recorded, func(function provider.Function) bool { return function.Name == "fn--web--api" })
	if at < 0 || !recorded[at].IgnoresGate {
		t.Errorf("the stack records %+v, want fn--web--api recorded as ignoring the gate: its service is live and answers anyone", recorded)
	}
}

func TestAnOpenPreviewOfOnlyFunctionsThatIgnoreTheGateNeedsNoGateSecretAndGrantsNoReadOfIt(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	functions, err := p.ProvisionFunctions(context.Background(), pythonFunctionsSpec(p, "fn--web--api"), &fake.Log{})
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v, want a preview whose services mount no gate to deploy without the gate's secret", err)
	}

	if len(functions) != 1 || !functions[0].IgnoresGate {
		t.Errorf("ProvisionFunctions() = %+v, want the function recorded as ignoring the gate", functions)
	}
	member := "serviceAccount:" + names(t, p).AppAccountEmail(environment.TierPreview, "shop", "pr-7", "web")
	if server.secrets != nil && slices.Contains(server.secrets.members(names(t, p).GateSecret(environment.TierPreview, "shop"), gateReaderRole), member) {
		t.Errorf("the app's account %s may read the gate secret, and none of its services mounts it", member)
	}
}

func TestAPreviewContainerWhoseServiceCannotBeReadIsNotDeployedAndMintsNoProbeToken(t *testing.T) {
	server := &runServer{refuseReads: true}
	p := server.open(t)
	publishGate(t, p, nil)

	_, err := p.ProvisionContainers(context.Background(), openPreviewSpec(p), &fake.Log{})

	if err == nil || !strings.Contains(err.Error(), "startup probe token") {
		t.Fatalf("ProvisionContainers() = %v, want the deploy to fail reading the service's startup probe token: a fresh token would replace the one its running instances probe with", err)
	}
	if len(server.created) != 0 || len(server.patched) != 0 {
		t.Errorf("the deploy wrote the service (%d created, %d patched), want nothing written with a token minted over an unread one", len(server.created), len(server.patched))
	}
}
