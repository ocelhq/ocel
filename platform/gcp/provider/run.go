package gcp

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	trafficByRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
	trafficField      = "traffic"
)

const (
	revisionCPU    = "1"
	revisionMemory = "512Mi"
)

const releaseAttempts = 4

const maxRequestTimeout = 3600 * time.Second

type serving struct {
	service     string
	image       string
	env         map[string]string
	account     string
	compute     provider.Compute
	health      string
	public      bool
	cpu         string
	memory      int
	concurrency int
	generation  string
	instances   provider.Instances
	timeout     time.Duration
	ingress     string
	mounts      []secretMount
	egress      *privateEgress
}

type privateEgress struct {
	network    string
	subnetwork string
}

const privateRangesOnly = "PRIVATE_RANGES_ONLY"

type secretMount struct {
	name   string
	secret string
	dir    string
	file   string
}

func volumesOf(mounts []secretMount) ([]*run.GoogleCloudRunV2Volume, []*run.GoogleCloudRunV2VolumeMount) {
	volumes := make([]*run.GoogleCloudRunV2Volume, 0, len(mounts))
	mounted := make([]*run.GoogleCloudRunV2VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		volumes = append(volumes, &run.GoogleCloudRunV2Volume{
			Name: mount.name,
			Secret: &run.GoogleCloudRunV2SecretVolumeSource{
				Secret: mount.secret,
				Items:  []*run.GoogleCloudRunV2VersionToPath{{Version: "latest", Path: mount.file}},
			},
		})
		mounted = append(mounted, &run.GoogleCloudRunV2VolumeMount{Name: mount.name, MountPath: mount.dir})
	}
	return volumes, mounted
}

const (
	ingressEverywhere   = "INGRESS_TRAFFIC_ALL"
	ingressLoadBalancer = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
	ingressInternal     = "INGRESS_TRAFFIC_INTERNAL_ONLY"
)

func ingressFor(facts edge.Facts) string {
	if facts.ShieldsOrigin {
		return ingressLoadBalancer
	}
	return ingressEverywhere
}

func factsOf(front edge.Edge) edge.Facts {
	if front == nil {
		return edge.Facts{}
	}
	return front.Facts()
}

func serviceOf(s serving) (*run.GoogleCloudRunV2Service, error) {
	memory := revisionMemory
	if s.memory > 0 {
		memory = strconv.Itoa(s.memory) + "Mi"
	}
	cpu := revisionCPU
	if s.cpu != "" {
		cpu = s.cpu
	}
	container := &run.GoogleCloudRunV2Container{
		Image: s.image,
		Ports: []*run.GoogleCloudRunV2ContainerPort{{ContainerPort: containerimage.Port}},
		Env:   environmentOf(s.env),
		Resources: &run.GoogleCloudRunV2ResourceRequirements{
			CpuIdle:         s.compute == provider.ComputeServerless,
			Limits:          map[string]string{"cpu": cpu, "memory": memory},
			ForceSendFields: []string{"CpuIdle"},
		},
	}
	scaling := &run.GoogleCloudRunV2RevisionScaling{MinInstanceCount: int64(s.instances.Min), ForceSendFields: []string{"MinInstanceCount"}}
	if s.instances.Max > 0 {
		scaling.MaxInstanceCount = int64(s.instances.Max)
	}
	if s.compute == provider.ComputeContainer {
		container.StartupProbe = &run.GoogleCloudRunV2Probe{
			HttpGet: &run.GoogleCloudRunV2HTTPGetAction{Path: s.health, Port: containerimage.Port},
		}
	}
	volumes, mounted := volumesOf(s.mounts)
	container.VolumeMounts = mounted
	template := &run.GoogleCloudRunV2RevisionTemplate{
		Containers:                    []*run.GoogleCloudRunV2Container{container},
		Scaling:                       scaling,
		ServiceAccount:                s.account,
		Volumes:                       volumes,
		MaxInstanceRequestConcurrency: int64(s.concurrency),
		ExecutionEnvironment:          s.generation,
	}
	if s.egress != nil {
		template.VpcAccess = &run.GoogleCloudRunV2VpcAccess{
			Egress:            privateRangesOnly,
			NetworkInterfaces: []*run.GoogleCloudRunV2NetworkInterface{{Network: s.egress.network, Subnetwork: s.egress.subnetwork}},
		}
	}
	if s.timeout > 0 {
		if s.timeout > maxRequestTimeout {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s asks for a request to run for %s, and Cloud Run cuts one off at %s: "+
					"ask for less, or move work that outlives a request off the request",
				s.service, s.timeout, maxRequestTimeout)
		}
		template.Timeout = strconv.Itoa(int(math.Ceil(s.timeout.Seconds()))) + "s"
	}
	ingress := s.ingress
	if ingress == "" {
		ingress = ingressEverywhere
	}
	return &run.GoogleCloudRunV2Service{
		Template:           template,
		Ingress:            ingress,
		InvokerIamDisabled: s.public,
		ForceSendFields:    []string{"InvokerIamDisabled"},
	}, nil
}

func environmentOf(values map[string]string) []*run.GoogleCloudRunV2EnvVar {
	entries := make([]*run.GoogleCloudRunV2EnvVar, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		entries = append(entries, &run.GoogleCloudRunV2EnvVar{Name: name, Value: values[name]})
	}
	return entries
}

func trafficTo(revision string) []*run.GoogleCloudRunV2TrafficTarget {
	return []*run.GoogleCloudRunV2TrafficTarget{
		{Type: trafficByRevision, Revision: revision, Percent: 100},
	}
}

func (p *Provider) storedAs(image string) string {
	repository, digest, pinned := strings.Cut(image, "@")
	if !pinned || !p.emulated() {
		return image
	}
	return repository + ":" + naming.DigestTag(digest)
}

type release struct {
	url      string
	revision string
}

func (p *Provider) openRun(ctx context.Context) (*clients, *run.Service, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return nil, nil, err
	}
	services, err := clients.Run()
	return clients, services, err
}

func (p *Provider) deployService(ctx context.Context, s serving, progress progress.Log) (release, error) {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return release{}, err
	}
	path := clients.servicePath(s.service)
	s.image = p.storedAs(s.image)
	desired, err := serviceOf(s)
	if err != nil {
		return release{}, err
	}

	_, err = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	switch {
	case absent(err):
		ensureProgress(progress).Say("Creating Cloud Run service " + s.service + " in " + clients.region)
		err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.
				Create(clients.location(), desired).ServiceId(s.service).Context(ctx).Do(call...)
		})
	case err != nil:
		return release{}, fmt.Errorf("read the Cloud Run service %s: %w", s.service, err)
	default:
		ensureProgress(progress).Say("Releasing a new revision of Cloud Run service " + s.service + " in " + clients.region)
		err = p.retryWrite(ctx, "release "+s.service+" onto Cloud Run", func() error {
			current, err := p.read(ctx, services, path, s.service)
			if err != nil {
				return err
			}
			desired.Etag = current.Etag
			desired.Traffic = heldTraffic(current)
			return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
				return services.Projects.Locations.Services.Patch(path, desired).Context(ctx).Do(call...)
			})
		})
	}
	if err != nil {
		return release{}, err
	}
	deployed, err := p.read(ctx, services, path, s.service)
	if err != nil {
		return release{}, err
	}
	revision, err := latestReady(s.service)(deployed)
	if err != nil {
		return release{}, err
	}
	return release{url: deployed.Uri, revision: revision}, nil
}

func heldTraffic(current *run.GoogleCloudRunV2Service) []*run.GoogleCloudRunV2TrafficTarget {
	serving := revisionName(current.LatestReadyRevision)
	if serving == "" || len(current.Traffic) > 0 && !slices.ContainsFunc(current.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Type != trafficByRevision
	}) {
		return current.Traffic
	}
	return trafficTo(serving)
}

func (p *Provider) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	if revision == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s is asked to serve a revision nothing named, and traffic is pinned to one revision by name", service)
	}
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	_, _, err = p.route(ctx, services, clients.servicePath(service), service,
		"pin the traffic of "+service+" to "+revision, activeNamed(ctx, revision, stillActive))
	return err
}

func latestReady(service string) func(*run.GoogleCloudRunV2Service) (string, error) {
	return func(current *run.GoogleCloudRunV2Service) (string, error) {
		revision := revisionName(current.LatestReadyRevision)
		if revision == "" {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"%s created no revision that came ready, and a release routes traffic to the revision it made rather than to whatever ran last",
				service)
		}
		return revision, nil
	}
}

func activeNamed(ctx context.Context, revision string, stillActive router.StillActive) func(*run.GoogleCloudRunV2Service) (string, error) {
	return func(*run.GoogleCloudRunV2Service) (string, error) {
		if stillActive == nil {
			return revision, nil
		}
		return revision, stillActive(ctx)
	}
}

func (p *Provider) route(
	ctx context.Context,
	services *run.Service,
	path, service, doing string,
	choose func(*run.GoogleCloudRunV2Service) (string, error),
) (*run.GoogleCloudRunV2Service, string, error) {
	var (
		latest   *run.GoogleCloudRunV2Service
		revision string
	)
	err := p.retryWrite(ctx, doing, func() error {
		current, err := p.read(ctx, services, path, service)
		if err != nil {
			return err
		}
		latest = current
		revision, err = choose(current)
		if err != nil {
			return err
		}
		if servedBy(current.Traffic, revision) {
			return nil
		}
		routed := &run.GoogleCloudRunV2Service{
			Etag:    current.Etag,
			Traffic: trafficTo(revision),
		}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, routed).UpdateMask(trafficField).Context(ctx).Do(call...)
		})
	})
	if err != nil {
		return nil, "", err
	}
	return latest, revision, nil
}

func (p *Provider) read(ctx context.Context, services *run.Service, path, service string) (*run.GoogleCloudRunV2Service, error) {
	found, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", service, err)
	}
	return found, nil
}

func (p *Provider) retryWrite(ctx context.Context, doing string, write func() error) error {
	var refused error
	for attempt := range releaseAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		if refused = write(); refused == nil || !stale(refused) {
			return refused
		}
	}
	return fmt.Errorf("%s, and it kept changing under this release: %w", doing, refused)
}

func stale(err error) bool {
	switch answeredCode(err) {
	case http.StatusConflict, http.StatusPreconditionFailed:
		return true
	}
	return status.Code(err) == codes.Aborted || status.Code(err) == codes.FailedPrecondition
}

func servedBy(traffic []*run.GoogleCloudRunV2TrafficTarget, revision string) bool {
	return len(traffic) == 1 &&
		traffic[0].Type == trafficByRevision &&
		revisionName(traffic[0].Revision) == revision &&
		traffic[0].Percent == 100
}

func revisionName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func (p *Provider) await(ctx context.Context, services *run.Service, call func(...googleapi.CallOption) (*run.GoogleLongrunningOperation, error)) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Cloud Run to release: %w", err)
	}
	finished, err := until(ctx, "Cloud Run to finish "+revisionName(started.Name), func() (*run.GoogleLongrunningOperation, error) {
		if started.Done {
			return started, nil
		}
		return attempted(ctx, func(opt ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Operations.Get(started.Name).Context(ctx).Do(opt...)
		})
	}, func(op *run.GoogleLongrunningOperation) bool { return op != nil && op.Done })
	if err != nil {
		return err
	}
	if finished.Error != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"Cloud Run refused the release: %s", finished.Error.Message)
	}
	return nil
}

func (p *Provider) tearDown(ctx context.Context, service string, progress progress.Log) error {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	ensureProgress(progress).Say("Deleting Cloud Run service " + service + " in " + clients.region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Delete(clients.servicePath(service)).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil
	}
	return err
}

func (p *Provider) removeRevision(ctx context.Context, service, revision string, progress progress.Log) (bool, error) {
	if service == "" || revision == "" {
		return false, nil
	}
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return false, err
	}
	path := clients.servicePath(service)
	current, err := p.read(ctx, services, path, service)
	if absent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if isLatestOrRouted(current, revision) {
		return true, nil
	}
	ensureProgress(progress).Say("Deleting revision " + revision + " of Cloud Run service " + service + " in " + clients.region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Revisions.Delete(path + "/revisions/" + revision).Context(ctx).Do(call...)
	})
	if absent(err) {
		return false, nil
	}
	return false, err
}

func isLatestOrRouted(service *run.GoogleCloudRunV2Service, revision string) bool {
	if revisionName(service.LatestReadyRevision) == revision || revisionName(service.LatestCreatedRevision) == revision {
		return true
	}
	return slices.ContainsFunc(service.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return revisionName(target.Revision) == revision
	})
}
