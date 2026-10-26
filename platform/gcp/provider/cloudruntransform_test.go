package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func servicePatched(patch map[string]any) *patching {
	return &patching{patches: transform.Patches{"service": patch}}
}

func evaluatedFor(t *testing.T, p *Provider, spec provider.StackSpec) context.Context {
	t.Helper()
	ctx, err := p.evaluateTransforms(context.Background(), spec)
	if err != nil {
		t.Fatalf("evaluateTransforms() = %v", err)
	}
	return ctx
}

func provisionTransformedFunctions(t *testing.T, p *Provider, spec provider.StackSpec) error {
	t.Helper()
	ctx, err := p.evaluateTransforms(context.Background(), spec)
	if err != nil {
		return err
	}
	_, err = p.ProvisionFunctions(ctx, spec, &fake.Log{})
	return err
}

func provisionTransformedContainers(t *testing.T, p *Provider, spec provider.StackSpec) error {
	t.Helper()
	ctx, err := p.evaluateTransforms(context.Background(), spec)
	if err != nil {
		return err
	}
	_, err = p.ProvisionContainers(ctx, spec, &fake.Log{})
	return err
}

func servingTransformed(t *testing.T, server *googletest.Run, pass transform.Pass) *Provider {
	t.Helper()
	p := providerServing(server, t)
	p.transforms = pass
	return p
}

func withWorker(spec provider.StackSpec, name string) provider.StackSpec {
	spec.App.Workers = append(spec.App.Workers, provider.WorkerSpec{Name: name})
	return spec
}

func TestATransformIsAskedAboutEveryCloudRunServiceAnAppRuns(t *testing.T) {
	for name, tc := range map[string]struct {
		spec provider.StackSpec
		want []transform.Resource
	}{
		"functions": {
			spec: withWorker(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), "media"),
			want: []transform.Resource{{Type: "function", Name: "fn--api--index", App: "api"}, {Type: "worker", Name: "media", App: "api"}},
		},
		"a container": {
			spec: withWorker(containerStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), "media"),
			want: []transform.Resource{{Type: "container", Name: "api", App: "api"}, {Type: "worker", Name: "media", App: "api"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			pass := &patching{}
			p := servingTransformed(t, &googletest.Run{}, pass)

			evaluatedFor(t, p, tc.spec)

			if !slices.Equal(pass.seen.Resources, tc.want) {
				t.Errorf("the transform was offered %v, want %v: each Cloud Run service of the app, named so an if gate can pick its app", pass.seen.Resources, tc.want)
			}
		})
	}
}

func TestATransformedFunctionIsReleasedWithWhatThePatchSet(t *testing.T) {
	server := &googletest.Run{}
	pass := servicePatched(map[string]any{
		"template": map[string]any{
			"containers": []any{map[string]any{"resources": map[string]any{
				"limits":          map[string]any{"cpu": "2", "memory": "2Gi", "nvidia.com/gpu": "1"},
				"cpuIdle":         false,
				"startupCpuBoost": true,
			}}},
			"scaling":                       map[string]any{"minInstanceCount": 1, "maxInstanceCount": 7},
			"maxInstanceRequestConcurrency": 40,
			"executionEnvironment":          "EXECUTION_ENVIRONMENT_GEN2",
			"sessionAffinity":               true,
			"timeout":                       "900s",
			"nodeSelector":                  map[string]any{"accelerator": "nvidia-l4"},
			"encryptionKey":                 "projects/acme-prod/locations/europe-west1/keyRings/apps/cryptoKeys/api",
		},
		"binaryAuthorization": map[string]any{"useDefault": true},
	})
	pass.tags = map[string]string{"team": "payments"}
	p := servingTransformed(t, server, pass)

	if err := provisionTransformedFunctions(t, p, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	created := server.Created[0]
	template := created.Template
	resources := template.Containers[0].Resources
	if resources.Limits["cpu"] != "2" || resources.Limits["memory"] != "2Gi" || resources.Limits["nvidia.com/gpu"] != "1" || resources.CpuIdle || !resources.StartupCpuBoost {
		t.Errorf("released resources %s, want 2 CPUs, 2Gi, a GPU, CPU kept between requests and boosted at startup", googletest.JSON(t, resources))
	}
	if template.Scaling.MinInstanceCount != 1 || template.Scaling.MaxInstanceCount != 7 || template.MaxInstanceRequestConcurrency != 40 {
		t.Errorf("released scaling %s and %d requests at once, want 1 to 7 instances taking 40 each", googletest.JSON(t, template.Scaling), template.MaxInstanceRequestConcurrency)
	}
	if template.ExecutionEnvironment != "EXECUTION_ENVIRONMENT_GEN2" || !template.SessionAffinity || template.Timeout != "900s" {
		t.Errorf("released %q, session affinity %v and timeout %q, want the second generation, affinity on and 900s", template.ExecutionEnvironment, template.SessionAffinity, template.Timeout)
	}
	if template.NodeSelector == nil || template.NodeSelector.Accelerator != "nvidia-l4" || template.EncryptionKey != "projects/acme-prod/locations/europe-west1/keyRings/apps/cryptoKeys/api" {
		t.Errorf("released node selector %s and key %q, want an nvidia-l4 and the patch's key", googletest.JSON(t, template.NodeSelector), template.EncryptionKey)
	}
	if created.BinaryAuthorization == nil || !created.BinaryAuthorization.UseDefault {
		t.Errorf("released binary authorization %s, want the default policy", googletest.JSON(t, created.BinaryAuthorization))
	}
	if created.Labels["team"] != "payments" || created.Labels["ocel-app"] != "api" {
		t.Errorf("released labels %v, want the rule's tag beside ocel's own", created.Labels)
	}
	if template.Containers[0].Image == "" || len(template.Containers[0].Env) == 0 {
		t.Errorf("released container %s, want ocel's image and env kept beside the patch", googletest.JSON(t, template.Containers[0]))
	}
}

func refusedNaming(t *testing.T, err error, named ...string) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("got %v, want an invalid refusal", err)
	}
	for _, name := range named {
		if !strings.Contains(refused.Message, name) {
			t.Errorf("refused with %q, want it to name %q", refused.Message, name)
		}
	}
}

func TestATransformSettingAWorkersTimeoutIsRefused(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"timeout": "60s"}}))

	err := provisionTransformedFunctions(t, p, withWorker(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), "media"))

	refusedNaming(t, err, "worker media", "service.template.timeout")
	if len(server.Created) != 0 {
		t.Errorf("created %d services, want none: a worker's timeout is the push deadline ocel keeps", len(server.Created))
	}
}

func TestATransformedWorkerIsReleasedWithWhatThePatchSet(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"maxInstanceRequestConcurrency": 3}}))
	spec := withWorker(containerStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), "media")

	if err := provisionTransformedContainers(t, p, spec); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	released := releasedTemplates(server)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container's and the worker's", len(released))
	}
	for _, service := range released {
		if service.Template.MaxInstanceRequestConcurrency != 3 {
			t.Errorf("a service takes %d requests at once, want the patch's 3", service.Template.MaxInstanceRequestConcurrency)
		}
	}
	if worker := released[1]; worker.Ingress != "INGRESS_TRAFFIC_INTERNAL_ONLY" || worker.Template.Timeout == "" {
		t.Errorf("the worker is released with ingress %q and timeout %q, want ocel's internal ingress and push deadline kept", worker.Ingress, worker.Template.Timeout)
	}
}

func TestATransformListingASecondContainerIsRefused(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"containers": []any{
		map[string]any{"resources": map[string]any{"cpuIdle": true}},
		map[string]any{"resources": map[string]any{"cpuIdle": true}},
	}}}))

	err := provisionTransformedContainers(t, p, containerStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	refusedNaming(t, err, "container api", "service.template.containers", "exactly one")
}

func TestATransformAskingForALongerRequestThanCloudRunServesIsRefused(t *testing.T) {
	p := servingTransformed(t, &googletest.Run{}, servicePatched(map[string]any{"template": map[string]any{"timeout": "7200s"}}))

	err := provisionTransformedFunctions(t, p, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	refusedNaming(t, err, "function fn--api--index", "2h0m0s", "1h0m0s")
}

func TestAPlanOfAnAppWhosePatchCloudRunCannotTakeIsRefused(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"executionEnvironment": "GEN3"}}))

	_, err := p.Stacks().Plan(context.Background(), functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), &fake.Log{})

	refusedNaming(t, err, "function fn--api--index", "service.template.executionEnvironment", "GEN3")
}

func TestATransformScalingPastWhatTheAppsDatabaseConnectionsFitIsRefusedNamingBoth(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"scaling": map[string]any{"maxInstanceCount": 20}}}))
	spec := boundToADatabase(productionSpec())
	spec.App.Compute = provider.ComputeContainer

	err := provisionTransformedContainers(t, p, spec)

	refusedNaming(t, err, "container web", "20", "at most 9")
	if len(server.Created) != 0 {
		t.Errorf("created %d services, want none", len(server.Created))
	}
}

func TestANextFunctionWhosePatchRaisesItsMemoryIsToldTheMemoryItRunsWith(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"containers": []any{
		map[string]any{"resources": map[string]any{"limits": map[string]any{"memory": "4Gi"}}},
	}}}))

	if err := provisionTransformedFunctions(t, p, nextSpec()); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	container := server.Created[0].Template.Containers[0]
	if container.Resources.Limits["memory"] != "4Gi" {
		t.Errorf("released memory %q, want the patch's 4Gi", container.Resources.Limits["memory"])
	}
	if told := googletest.EnvOf(container)["OCEL_FUNCTION_MEMORY_MB"]; told != "4096" {
		t.Errorf("the function is told it has %q MB, want 4096: Next sizes its cache from the memory it runs with", told)
	}
}

func releasedTemplates(server *googletest.Run) []*runv2.GoogleCloudRunV2Service {
	var released []*runv2.GoogleCloudRunV2Service
	for _, service := range releasedServices(server) {
		if service.Template != nil {
			released = append(released, service)
		}
	}
	return released
}

func TestAServiceWhosePatchPlacesItInANetworkGetsThatNetworkAndNoEgressOfOcels(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, servicePatched(map[string]any{"template": map[string]any{"vpcAccess": map[string]any{
		"egress": "ALL_TRAFFIC",
		"networkInterfaces": []any{map[string]any{
			"network":    "apps",
			"subnetwork": "projects/acme-prod/regions/europe-west1/subnetworks/apps-eu",
			"tags":       []any{"egress-nat"},
		}},
	}}}))
	spec := boundToADatabase(productionSpec())
	spec.App.Compute = provider.ComputeContainer

	if err := provisionTransformedContainers(t, p, spec); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	access := server.Created[0].Template.VpcAccess
	want := &runv2.GoogleCloudRunV2VpcAccess{Egress: "ALL_TRAFFIC", NetworkInterfaces: []*runv2.GoogleCloudRunV2NetworkInterface{{
		Network:    "projects/acme-prod/global/networks/apps",
		Subnetwork: "projects/acme-prod/regions/europe-west1/subnetworks/apps-eu",
		Tags:       []string{"egress-nat"},
	}}}
	if googletest.JSON(t, access) != googletest.JSON(t, want) {
		t.Errorf("released VPC access %s, want exactly the patch's %s: a placed service reaches only the network its patch names", googletest.JSON(t, access), googletest.JSON(t, want))
	}
}

func TestAPatchPlacingAServiceInAnotherProjectsNetworkIsRefused(t *testing.T) {
	p := servingTransformed(t, &googletest.Run{}, servicePatched(map[string]any{"template": map[string]any{"vpcAccess": map[string]any{
		"networkInterfaces": []any{map[string]any{"network": "projects/host-vpc/global/networks/shared"}},
	}}}))

	err := provisionTransformedFunctions(t, p, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	refusedNaming(t, err, "projects/host-vpc/global/networks/shared", "acme-prod")
}
