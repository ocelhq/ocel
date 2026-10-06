package gcp

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	opensOnPromotionLabel = "ocel-opens-on-promotion"
	invokerCheckField     = "invoker_iam_disabled"
	proxyField            = "iap_enabled"
	opensToEveryone       = "everyone"
	opensToViewers        = "viewers"
)

const (
	trafficByRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
	trafficByLatest   = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
	trafficField      = "traffic"
)

const (
	revisionCPU    = "1"
	revisionMemory = "512Mi"
)

const releaseAttempts = 4

const maxRequestTimeout = 3600 * time.Second

type serving struct {
	service        string
	image          string
	env            map[string]string
	account        string
	compute        provider.Compute
	health         string
	public         bool
	cpu            string
	memory         int
	concurrency    int
	generation     string
	instances      provider.Instances
	timeout        time.Duration
	ingress        string
	mounts         []secretMount
	egress         *privateEgress
	tag            string
	iap            bool
	instanceBilled bool
	labels         map[string]string

	opensOnPromotion bool
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

func memoryLimit(mb int) string {
	return strconv.Itoa(mb) + "Mi"
}

func serviceOf(s serving) (*run.GoogleCloudRunV2Service, error) {
	memory := revisionMemory
	if s.memory > 0 {
		memory = memoryLimit(s.memory)
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
			CpuIdle:         s.billsPerRequest(),
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
		Labels:                        map[string]string{imageLabel: imageLabelValue(s.image)},
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
	service := &run.GoogleCloudRunV2Service{
		Template:           template,
		Ingress:            ingress,
		InvokerIamDisabled: s.public && !s.opensOnPromotion && !s.iap,
		IapEnabled:         s.iap && !s.opensOnPromotion,
		ForceSendFields:    []string{"InvokerIamDisabled", "IapEnabled"},
		Labels:             maps.Clone(s.labels),
	}
	opening := ""
	switch {
	case !s.opensOnPromotion:
	case s.iap:
		opening = opensToViewers
	case s.public:
		opening = opensToEveryone
	}
	if opening != "" {
		if service.Labels == nil {
			service.Labels = map[string]string{}
		}
		service.Labels[opensOnPromotionLabel] = opening
	}
	return service, nil
}

func isOpen(service *run.GoogleCloudRunV2Service) bool {
	return service.InvokerIamDisabled || service.IapEnabled
}

func openingOf(service *run.GoogleCloudRunV2Service) (*run.GoogleCloudRunV2Service, string) {
	switch service.Labels[opensOnPromotionLabel] {
	case opensToEveryone:
		return &run.GoogleCloudRunV2Service{InvokerIamDisabled: true}, invokerCheckField
	case opensToViewers:
		return &run.GoogleCloudRunV2Service{IapEnabled: true}, proxyField
	}
	return nil, ""
}

func environmentOf(values map[string]string) []*run.GoogleCloudRunV2EnvVar {
	entries := make([]*run.GoogleCloudRunV2EnvVar, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		entries = append(entries, &run.GoogleCloudRunV2EnvVar{Name: name, Value: values[name]})
	}
	return entries
}

func trafficTo(revision string, current []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
	pinned := &run.GoogleCloudRunV2TrafficTarget{Type: trafficByRevision, Revision: revision, Percent: 100}
	traffic := []*run.GoogleCloudRunV2TrafficTarget{pinned}
	for _, target := range current {
		if target.Tag == "" {
			continue
		}
		if pinned.Tag == "" && target.Type == trafficByRevision && revisionName(target.Revision) == revision {
			pinned.Tag = target.Tag
			continue
		}
		traffic = append(traffic, &run.GoogleCloudRunV2TrafficTarget{Type: target.Type, Revision: target.Revision, Tag: target.Tag})
	}
	return traffic
}

type release struct {
	url           string
	deploymentURL string
	revision      string
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
	desired, err := serviceOf(s)
	if err != nil {
		return release{}, err
	}
	if err := p.ensureImageTag(ctx, clients, s.image); err != nil {
		return release{}, err
	}
	err = p.writeRelease(ctx, clients, services, s, desired, progress)
	if isImageMissing(err, s.image) {
		ensureProgress(progress).Say("Cloud Run found no image " + s.image + ", which a prune untagged during this release: tagging it again and releasing " + s.service + " once more")
		if err := p.ensureImageTag(ctx, clients, s.image); err != nil {
			return release{}, err
		}
		desired.Template.Labels[imageRetaggedLabel] = "true"
		err = p.writeRelease(ctx, clients, services, s, desired, progress)
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
	if s.tag == "" {
		return release{url: deployed.Uri, revision: revision}, nil
	}
	if err := p.tagRevision(ctx, services, path, s.service, revision, s.tag); err != nil {
		return release{}, err
	}
	if !s.iap {
		return release{url: deployed.Uri, revision: revision}, nil
	}
	tagged, err := p.read(ctx, services, path, s.service)
	if err != nil {
		return release{}, err
	}
	return release{url: deployed.Uri, deploymentURL: taggedAddress(tagged, s.tag), revision: revision}, nil
}

func taggedAddress(current *run.GoogleCloudRunV2Service, tag string) string {
	for _, status := range current.TrafficStatuses {
		if status.Tag == tag {
			return status.Uri
		}
	}
	return ""
}

func (p *Provider) writeRelease(ctx context.Context, clients *clients, services *run.Service, s serving, desired *run.GoogleCloudRunV2Service, progress progress.Log) error {
	path := clients.servicePath(s.service)
	_, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
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
		return fmt.Errorf("read the Cloud Run service %s: %w", s.service, err)
	default:
		ensureProgress(progress).Say("Releasing a new revision of Cloud Run service " + s.service + " in " + clients.region)
		err = p.retryWrite(ctx, "release "+s.service+" onto Cloud Run", func() error {
			current, err := p.read(ctx, services, path, s.service)
			if err != nil {
				return err
			}
			attempt := *desired
			attempt.Etag = current.Etag
			attempt.Traffic = heldTraffic(current)
			if recorded, found := current.Annotations[rollbacksAnnotation]; found {
				attempt.Annotations = map[string]string{rollbacksAnnotation: recorded}
			}
			if opened, _ := openingOf(desired); opened != nil && isOpen(current) {
				attempt.InvokerIamDisabled, attempt.IapEnabled = opened.InvokerIamDisabled, opened.IapEnabled
			}
			return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
				return services.Projects.Locations.Services.Patch(path, &attempt).Context(ctx).Do(call...)
			})
		})
	}
	return err
}

func (p *Provider) tagRevision(ctx context.Context, services *run.Service, path, service, revision, tag string) error {
	return p.rewriteTraffic(ctx, services, path, service, "tag revision "+revision+" of "+service+" as "+tag,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return withTag(traffic, revision, tag)
		})
}

func (p *Provider) untagRevision(ctx context.Context, services *run.Service, path, service, revision string) error {
	return p.rewriteTraffic(ctx, services, path, service, "take every tag off revision "+revision+" of "+service,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return slices.DeleteFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
				return target.Percent == 0 && target.Tag != "" && revisionName(target.Revision) == revision
			})
		})
}

func (p *Provider) rewriteTraffic(
	ctx context.Context,
	services *run.Service,
	path, service, doing string,
	change func([]*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget,
) error {
	return p.retryWrite(ctx, doing, func() error {
		current, err := p.read(ctx, services, path, service)
		if err != nil {
			return err
		}
		traffic := change(slices.Clone(allocatedTraffic(current)))
		if sameTraffic(traffic, allocatedTraffic(current)) {
			return nil
		}
		rewritten := &run.GoogleCloudRunV2Service{Etag: current.Etag, Traffic: traffic}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, rewritten).UpdateMask(trafficField).Context(ctx).Do(call...)
		})
	})
}

func allocatedTraffic(current *run.GoogleCloudRunV2Service) []*run.GoogleCloudRunV2TrafficTarget {
	if len(current.Traffic) > 0 {
		return current.Traffic
	}
	return []*run.GoogleCloudRunV2TrafficTarget{{Type: trafficByLatest, Percent: 100}}
}

func withTag(traffic []*run.GoogleCloudRunV2TrafficTarget, revision, tag string) []*run.GoogleCloudRunV2TrafficTarget {
	if slices.ContainsFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Tag == tag && target.Type == trafficByRevision && revisionName(target.Revision) == revision
	}) {
		return traffic
	}
	return append(withoutTag(traffic, tag), &run.GoogleCloudRunV2TrafficTarget{Type: trafficByRevision, Revision: revision, Tag: tag})
}

func sameTraffic(a, b []*run.GoogleCloudRunV2TrafficTarget) bool {
	return slices.EqualFunc(a, b, func(x, y *run.GoogleCloudRunV2TrafficTarget) bool {
		return x.Type == y.Type && revisionName(x.Revision) == revisionName(y.Revision) && x.Percent == y.Percent && x.Tag == y.Tag
	})
}

func heldTraffic(current *run.GoogleCloudRunV2Service) []*run.GoogleCloudRunV2TrafficTarget {
	serving := revisionName(current.LatestReadyRevision)
	if serving == "" || len(current.Traffic) > 0 && !slices.ContainsFunc(current.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Type != trafficByRevision
	}) {
		return current.Traffic
	}
	return trafficTo(serving, current.Traffic)
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
	open bool,
) (bool, error) {
	var opened bool
	err := p.retryWrite(ctx, doing, func() error {
		opened = false
		current, err := p.read(ctx, services, path, service)
		if err != nil {
			return err
		}
		revision, err := choose(current)
		if err != nil {
			return err
		}
		routed, opening := openingOf(current)
		if !open || isOpen(current) {
			routed, opening = nil, ""
		}
		var annotations map[string]string
		cleared := false
		if open {
			if annotations, cleared, err = withoutRollback(current, revision); err != nil {
				return err
			}
		}
		if servedBy(current.Traffic, revision) && routed == nil && !cleared {
			return nil
		}
		if routed == nil {
			routed = &run.GoogleCloudRunV2Service{}
		}
		routed.Etag = current.Etag
		routed.Traffic = trafficTo(revision, current.Traffic)
		mask := trafficField
		if opening != "" {
			mask += "," + opening
		}
		if cleared {
			routed.Annotations = annotations
			routed.ForceSendFields = append(routed.ForceSendFields, "Annotations")
			mask += "," + annotationsField
		}
		opened = opening != ""
		err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, routed).UpdateMask(mask).Context(ctx).Do(call...)
		})
		if stale(err) {
			opened = false
		}
		return err
	})
	return opened, err
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
	var served int64
	for _, target := range traffic {
		if target.Percent == 0 {
			continue
		}
		if target.Type != trafficByRevision || revisionName(target.Revision) != revision {
			return false
		}
		served += target.Percent
	}
	return served == 100
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
	images, err := p.deleteService(ctx, service, progress)
	if err != nil {
		return err
	}
	p.untagUnusedImages(ctx, images, progress)
	return nil
}

func (p *Provider) deleteService(ctx context.Context, service string, progress progress.Log) ([]string, error) {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return nil, err
	}
	path := clients.servicePath(service)
	images, err := p.listServiceImages(ctx, services, path)
	if err != nil && !absent(err) {
		ensureProgress(progress).Warn(fmt.Sprintf("Leaving the images %s ran tagged, as which ones could not be read: %v", service, err))
	}
	ensureProgress(progress).Say("Deleting Cloud Run service " + service + " in " + clients.region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Delete(path).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return images, nil
}

func (p *Provider) removeRevision(ctx context.Context, service, revision string, progress progress.Log) (bool, []string, error) {
	if service == "" || revision == "" {
		return false, nil, nil
	}
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return false, nil, err
	}
	path := clients.servicePath(service)
	current, err := p.read(ctx, services, path, service)
	if absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if isLatestOrRouted(current, revision) {
		return true, nil, nil
	}
	images, err := p.readRevisionImages(ctx, services, path+"/revisions/"+revision)
	if absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("read which image revision %s of %s runs: %w", revision, service, err)
	}
	if err := p.untagRevision(ctx, services, path, service, revision); err != nil {
		return false, nil, err
	}
	ensureProgress(progress).Say("Deleting revision " + revision + " of Cloud Run service " + service + " in " + clients.region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Revisions.Delete(path + "/revisions/" + revision).Context(ctx).Do(call...)
	})
	if absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return false, images, nil
}

func isLatestOrRouted(service *run.GoogleCloudRunV2Service, revision string) bool {
	if revisionName(service.LatestReadyRevision) == revision || revisionName(service.LatestCreatedRevision) == revision {
		return true
	}
	return slices.ContainsFunc(service.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Percent > 0 && revisionName(target.Revision) == revision
	})
}

func (s serving) billsPerRequest() bool {
	return s.compute == provider.ComputeServerless && !s.instanceBilled
}
