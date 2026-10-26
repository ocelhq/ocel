package release

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	TransformFunction  = "function"
	TransformContainer = "container"
	TransformWorker    = "worker"

	serviceSurfaceName = "service"

	gpuLimit          = "nvidia.com/gpu"
	maxRequestsAtOnce = 1000
	computeAPIPrefix  = "https://www.googleapis.com/compute/v1/"
)

var (
	executionEnvironments = []string{"EXECUTION_ENVIRONMENT_GEN1", "EXECUTION_ENVIRONMENT_GEN2"}
	vpcEgresses           = []string{"ALL_TRAFFIC", "PRIVATE_RANGES_ONLY"}
)

type servicePlacement struct {
	project string
	region  string
}

type serviceSurface = surface.Surface[runv2.GoogleCloudRunV2Service, struct{}]

type serviceField = surface.Field[runv2.GoogleCloudRunV2Service, struct{}]

type servicePath = surface.Path[runv2.GoogleCloudRunV2Service]

func newServiceSurface(at servicePlacement, timed bool) serviceSurface {
	fields := []serviceField{
		{Name: "container resources", Paths: []servicePath{surface.NewPath("template.containers", patchContainers)}},
		{Name: "scaling", Paths: []servicePath{surface.NewPath("template.scaling", patchScaling)}},
		{Name: "request concurrency", Paths: []servicePath{surface.NewPath("template.maxInstanceRequestConcurrency", patchConcurrency)}},
		{Name: "execution environment", Paths: []servicePath{surface.NewPath("template.executionEnvironment", patchExecutionEnvironment)}},
		{Name: "session affinity", Paths: []servicePath{surface.NewPath("template.sessionAffinity", func(desired *runv2.GoogleCloudRunV2Service, on bool) error {
			desired.Template.SessionAffinity = on
			return nil
		})}},
		{Name: "accelerator", Paths: []servicePath{surface.NewPath("template.nodeSelector", patchNodeSelector)}},
		{Name: "encryption key", Paths: []servicePath{surface.NewPath("template.encryptionKey", patchEncryptionKey)}},
		{Name: "binary authorization", Paths: []servicePath{surface.NewPath("binaryAuthorization", patchBinaryAuthorization)}},
		{Name: "VPC access", Paths: []servicePath{surface.NewPath(surface.EgressPlacement, at.patchVPCAccess)}},
	}
	if timed {
		fields = append(fields, serviceField{Name: "request timeout", Paths: []servicePath{surface.NewPath("template.timeout", patchTimeout)}})
	}
	return serviceSurface{
		Name:   serviceSurfaceName,
		Fields: fields,
		Labels: &surface.Labels[runv2.GoogleCloudRunV2Service, struct{}]{
			Of: func(service *runv2.GoogleCloudRunV2Service) *map[string]string { return &service.Labels },
		},
	}
}

func Transformed() map[string]surface.Allowed {
	app := surface.Allowed{serviceSurfaceName: newServiceSurface(servicePlacement{}, true).Paths()}
	return map[string]surface.Allowed{
		TransformFunction:  app,
		TransformContainer: app,
		TransformWorker:    {serviceSurfaceName: newServiceSurface(servicePlacement{}, false).Paths()},
	}
}

func TransformCandidates(spec provider.StackSpec) []transform.Resource {
	app := spec.App
	if app == nil {
		return nil
	}
	functions := make([]string, 0, len(app.Functions))
	for _, fn := range app.Functions {
		functions = append(functions, fn.Name)
	}
	workers := make([]string, 0, len(app.Workers))
	for _, worker := range app.Workers {
		workers = append(workers, worker.Name)
	}
	return ServiceCandidates(app.App, app.Compute, functions, workers)
}

func ServiceCandidates(app string, compute provider.Compute, functions, workers []string) []transform.Resource {
	var candidates []transform.Resource
	if compute == provider.ComputeContainer {
		candidates = append(candidates, transform.Resource{Type: TransformContainer, Name: app, App: app})
	} else {
		for _, function := range functions {
			candidates = append(candidates, transform.Resource{Type: TransformFunction, Name: function, App: app})
		}
	}
	for _, worker := range workers {
		candidates = append(candidates, transform.Resource{Type: TransformWorker, Name: worker, App: app})
	}
	return candidates
}

type pulumiContainer struct {
	Resources struct {
		Limits struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
			GPU    string `json:"nvidia.com/gpu"`
		} `json:"limits"`
		CPUIdle         *bool `json:"cpuIdle"`
		StartupCPUBoost *bool `json:"startupCpuBoost"`
	} `json:"resources"`
}

func patchContainers(desired *runv2.GoogleCloudRunV2Service, containers []pulumiContainer) error {
	if len(containers) != 1 || len(desired.Template.Containers) != 1 {
		return fmt.Errorf("the service runs one container and the patch lists %d: patch exactly one", len(containers))
	}
	patch, container := containers[0].Resources, desired.Template.Containers[0]
	if container.Resources == nil {
		container.Resources = &runv2.GoogleCloudRunV2ResourceRequirements{}
	}
	resources := container.Resources
	for name, quantity := range map[string]string{"cpu": patch.Limits.CPU, "memory": patch.Limits.Memory, gpuLimit: patch.Limits.GPU} {
		if quantity == "" {
			continue
		}
		if !run.IsQuantity(quantity) {
			return fmt.Errorf("limits.%s %q is not a quantity such as 2, 1000m or 4Gi", name, quantity)
		}
		if resources.Limits == nil {
			resources.Limits = map[string]string{}
		}
		resources.Limits[name] = quantity
	}
	if patch.CPUIdle != nil {
		resources.CpuIdle = *patch.CPUIdle
		resources.ForceSendFields = append(resources.ForceSendFields, "CpuIdle")
	}
	if patch.StartupCPUBoost != nil {
		resources.StartupCpuBoost = *patch.StartupCPUBoost
		resources.ForceSendFields = append(resources.ForceSendFields, "StartupCpuBoost")
	}
	return nil
}

type pulumiScaling struct {
	MinInstanceCount *int64 `json:"minInstanceCount"`
	MaxInstanceCount *int64 `json:"maxInstanceCount"`
}

func patchScaling(desired *runv2.GoogleCloudRunV2Service, scaling pulumiScaling) error {
	if desired.Template.Scaling == nil {
		desired.Template.Scaling = &runv2.GoogleCloudRunV2RevisionScaling{}
	}
	held := desired.Template.Scaling
	if count := scaling.MinInstanceCount; count != nil {
		if *count < 0 {
			return fmt.Errorf("minInstanceCount %d is negative", *count)
		}
		held.MinInstanceCount = *count
		held.ForceSendFields = append(held.ForceSendFields, "MinInstanceCount")
	}
	if count := scaling.MaxInstanceCount; count != nil {
		if *count < 1 {
			return fmt.Errorf("maxInstanceCount %d is below 1", *count)
		}
		held.MaxInstanceCount = *count
	}
	if held.MaxInstanceCount > 0 && held.MinInstanceCount > held.MaxInstanceCount {
		return fmt.Errorf("minInstanceCount %d is above maxInstanceCount %d", held.MinInstanceCount, held.MaxInstanceCount)
	}
	return nil
}

func patchConcurrency(desired *runv2.GoogleCloudRunV2Service, requests int64) error {
	if requests < 1 || requests > maxRequestsAtOnce {
		return fmt.Errorf("%d requests at once is outside 1 to %d", requests, maxRequestsAtOnce)
	}
	desired.Template.MaxInstanceRequestConcurrency = requests
	return nil
}

func patchExecutionEnvironment(desired *runv2.GoogleCloudRunV2Service, environment string) error {
	if !slices.Contains(executionEnvironments, environment) {
		return fmt.Errorf("%q is none of %s", environment, strings.Join(executionEnvironments, ", "))
	}
	desired.Template.ExecutionEnvironment = environment
	return nil
}

func patchTimeout(desired *runv2.GoogleCloudRunV2Service, written string) error {
	timeout, err := time.ParseDuration(written)
	if err != nil || !strings.HasSuffix(written, "s") || timeout <= 0 {
		return fmt.Errorf("%q is not a number of seconds above zero such as 300s", written)
	}
	if timeout > run.MaxRequestTimeout {
		return fmt.Errorf("%s is longer than the %s Cloud Run lets a request run", timeout, run.MaxRequestTimeout)
	}
	desired.Template.Timeout = fmt.Sprintf("%ds", int(math.Ceil(timeout.Seconds())))
	return nil
}

type pulumiNodeSelector struct {
	Accelerator string `json:"accelerator"`
}

func patchNodeSelector(desired *runv2.GoogleCloudRunV2Service, selector pulumiNodeSelector) error {
	if selector.Accelerator == "" {
		return fmt.Errorf("the node selector names no accelerator")
	}
	desired.Template.NodeSelector = &runv2.GoogleCloudRunV2NodeSelector{Accelerator: selector.Accelerator}
	return nil
}

func patchEncryptionKey(desired *runv2.GoogleCloudRunV2Service, key string) error {
	if !strings.HasPrefix(key, "projects/") || !strings.Contains(key, "/cryptoKeys/") {
		return fmt.Errorf("%q is not a key path such as projects/acme/locations/europe-west1/keyRings/ring/cryptoKeys/key", key)
	}
	desired.Template.EncryptionKey = key
	return nil
}

type pulumiBinaryAuthorization struct {
	UseDefault              *bool  `json:"useDefault"`
	Policy                  string `json:"policy"`
	BreakglassJustification string `json:"breakglassJustification"`
}

func patchBinaryAuthorization(desired *runv2.GoogleCloudRunV2Service, authorization pulumiBinaryAuthorization) error {
	useDefault := authorization.UseDefault != nil && *authorization.UseDefault
	if useDefault && authorization.Policy != "" {
		return fmt.Errorf("binary authorization both uses the default policy and names %s: pick one", authorization.Policy)
	}
	desired.BinaryAuthorization = &runv2.GoogleCloudRunV2BinaryAuthorization{
		UseDefault:              useDefault,
		Policy:                  authorization.Policy,
		BreakglassJustification: authorization.BreakglassJustification,
	}
	return nil
}

type pulumiVPCAccess struct {
	Egress            string `json:"egress"`
	NetworkInterfaces []struct {
		Network    string   `json:"network"`
		Subnetwork string   `json:"subnetwork"`
		Tags       []string `json:"tags"`
	} `json:"networkInterfaces"`
}

func (at servicePlacement) patchVPCAccess(desired *runv2.GoogleCloudRunV2Service, access pulumiVPCAccess) error {
	if len(access.NetworkInterfaces) != 1 {
		return fmt.Errorf("the patch lists %d network interfaces, and a service reaches a VPC network through exactly one", len(access.NetworkInterfaces))
	}
	if access.Egress != "" && !slices.Contains(vpcEgresses, access.Egress) {
		return fmt.Errorf("egress %q is none of %s", access.Egress, strings.Join(vpcEgresses, ", "))
	}
	written := access.NetworkInterfaces[0]
	if written.Network == "" && written.Subnetwork == "" {
		return fmt.Errorf("the network interface names neither a network nor a subnetwork")
	}
	network, err := at.resolve(written.Network, "global", "networks", "")
	if err != nil {
		return err
	}
	subnetwork, err := at.resolve(written.Subnetwork, "regions/"+at.region, "subnetworks", " in region "+at.region)
	if err != nil {
		return err
	}
	desired.Template.VpcAccess = &runv2.GoogleCloudRunV2VpcAccess{
		Egress:            access.Egress,
		NetworkInterfaces: []*runv2.GoogleCloudRunV2NetworkInterface{{Network: network, Subnetwork: subnetwork, Tags: slices.Clone(written.Tags)}},
	}
	return nil
}

func (at servicePlacement) resolve(written, scope, collection, where string) (string, error) {
	if written == "" {
		return "", nil
	}
	path := strings.TrimPrefix(written, computeAPIPrefix)
	if !strings.Contains(path, "/") {
		return "projects/" + at.project + "/" + scope + "/" + collection + "/" + path, nil
	}
	want := "projects/" + at.project + "/" + scope + "/" + collection + "/"
	if name, found := strings.CutPrefix(path, want); found && name != "" && !strings.Contains(name, "/") {
		return path, nil
	}
	return "", fmt.Errorf("%s is not a %s of project %s%s, the one this deploy runs in: name one by its name or as %s<name>", written, strings.TrimSuffix(collection, "s"), at.project, where, want)
}
