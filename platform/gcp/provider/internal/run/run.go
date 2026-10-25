package run

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	OpensOnPromotionLabel = "ocel-opens-on-promotion"
	invokerCheckField     = "invoker_iam_disabled"
	opensToEveryone       = "everyone"
)

const (
	TrafficByRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
	TrafficByLatest   = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
	trafficField      = "traffic"
)

const (
	ReadyCondition     = "Ready"
	ConditionSucceeded = "CONDITION_SUCCEEDED"
)

const (
	RevisionCPU    = "1"
	RevisionMemory = "512Mi"
)

const ReleaseAttempts = 4

const MaxRequestTimeout = 3600 * time.Second

type Serving struct {
	Service     string
	Tier        environment.Tier
	Shared      bool
	Image       string
	Env         map[string]string
	Account     string
	Compute     provider.Compute
	Health      string
	ProbeToken  string
	Public      bool
	CPU         string
	Memory      int
	Concurrency int
	Generation  string
	Instances   provider.Instances
	Timeout     time.Duration
	Ingress     string
	Mounts      []SecretMount
	Egress      *PrivateEgress
	Tag         string
	Billing     Billing
	Labels      map[string]string
	Audiences   []string

	OpensOnPromotion bool
}

type PrivateEgress struct {
	Network    string
	Subnetwork string
}

const privateRangesOnly = "PRIVATE_RANGES_ONLY"

type SecretMount struct {
	Name   string
	Secret string
	Dir    string
	File   string
}

func volumesOf(mounts []SecretMount) ([]*run.GoogleCloudRunV2Volume, []*run.GoogleCloudRunV2VolumeMount) {
	volumes := make([]*run.GoogleCloudRunV2Volume, 0, len(mounts))
	mounted := make([]*run.GoogleCloudRunV2VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		volumes = append(volumes, &run.GoogleCloudRunV2Volume{
			Name: mount.Name,
			Secret: &run.GoogleCloudRunV2SecretVolumeSource{
				Secret: mount.Secret,
				Items:  []*run.GoogleCloudRunV2VersionToPath{{Version: "latest", Path: mount.File}},
			},
		})
		mounted = append(mounted, &run.GoogleCloudRunV2VolumeMount{Name: mount.Name, MountPath: mount.Dir})
	}
	return volumes, mounted
}

const (
	IngressEverywhere   = "INGRESS_TRAFFIC_ALL"
	IngressLoadBalancer = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
	IngressInternal     = "INGRESS_TRAFFIC_INTERNAL_ONLY"
)

func IngressFor(facts edge.Facts) string {
	if facts.ShieldsOrigin && !facts.RunsCode {
		return IngressLoadBalancer
	}
	return IngressEverywhere
}

func FactsOf(front edge.Edge) edge.Facts {
	if front == nil {
		return edge.Facts{}
	}
	return front.Facts()
}

func FormatMemoryLimit(mb int) string {
	return strconv.Itoa(mb) + "Mi"
}

func ServiceOf(s Serving) (*run.GoogleCloudRunV2Service, error) {
	memory := RevisionMemory
	if s.Memory > 0 {
		memory = FormatMemoryLimit(s.Memory)
	}
	cpu := RevisionCPU
	if s.CPU != "" {
		cpu = s.CPU
	}
	container := &run.GoogleCloudRunV2Container{
		Image: s.Image,
		Ports: []*run.GoogleCloudRunV2ContainerPort{{ContainerPort: containerimage.Port}},
		Env:   environmentOf(s.Env),
		Resources: &run.GoogleCloudRunV2ResourceRequirements{
			CpuIdle:         s.billsPerRequest(),
			Limits:          map[string]string{"cpu": cpu, "memory": memory},
			ForceSendFields: []string{"CpuIdle"},
		},
	}
	scaling := &run.GoogleCloudRunV2RevisionScaling{MinInstanceCount: int64(s.Instances.Min), ForceSendFields: []string{"MinInstanceCount"}}
	if s.Instances.Max > 0 {
		scaling.MaxInstanceCount = int64(s.Instances.Max)
	}
	if s.Compute == provider.ComputeContainer {
		container.StartupProbe = &run.GoogleCloudRunV2Probe{
			HttpGet: &run.GoogleCloudRunV2HTTPGetAction{Path: s.Health, Port: containerimage.Port},
		}
		if s.ProbeToken != "" {
			container.StartupProbe.HttpGet.HttpHeaders = []*run.GoogleCloudRunV2HTTPHeader{{Name: originguard.ProbeTokenHeader, Value: s.ProbeToken}}
		}
	}
	volumes, mounted := volumesOf(s.Mounts)
	container.VolumeMounts = mounted
	template := &run.GoogleCloudRunV2RevisionTemplate{
		Containers:                    []*run.GoogleCloudRunV2Container{container},
		Scaling:                       scaling,
		ServiceAccount:                s.Account,
		Volumes:                       volumes,
		MaxInstanceRequestConcurrency: int64(s.Concurrency),
		ExecutionEnvironment:          s.Generation,
		Labels:                        map[string]string{imageLabel: ImageLabelValue(s.Image)},
	}
	if s.Egress != nil {
		template.VpcAccess = &run.GoogleCloudRunV2VpcAccess{
			Egress:            privateRangesOnly,
			NetworkInterfaces: []*run.GoogleCloudRunV2NetworkInterface{{Network: s.Egress.Network, Subnetwork: s.Egress.Subnetwork}},
		}
	}
	if s.Timeout > 0 {
		if s.Timeout > MaxRequestTimeout {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s asks for a request to run for %s, and Cloud Run cuts one off at %s: "+
					"ask for less, or move work that outlives a request off the request",
				s.Service, s.Timeout, MaxRequestTimeout)
		}
		template.Timeout = strconv.Itoa(int(math.Ceil(s.Timeout.Seconds()))) + "s"
	}
	ingress := s.Ingress
	if ingress == "" {
		ingress = IngressEverywhere
	}
	service := &run.GoogleCloudRunV2Service{
		Template:           template,
		Ingress:            ingress,
		InvokerIamDisabled: s.Public && !s.OpensOnPromotion,
		ForceSendFields:    []string{"InvokerIamDisabled"},
		Labels:             maps.Clone(s.Labels),
		CustomAudiences:    slices.Clone(s.Audiences),
	}
	if s.OpensOnPromotion && s.Public {
		if service.Labels == nil {
			service.Labels = map[string]string{}
		}
		service.Labels[OpensOnPromotionLabel] = opensToEveryone
	}
	labelTier(service, s)
	stampServing(service)
	return service, nil
}

func labelTier(service *run.GoogleCloudRunV2Service, s Serving) {
	if s.Shared {
		return
	}
	if service.Labels == nil {
		service.Labels = map[string]string{}
	}
	service.Labels[names.TierLabel] = string(s.Tier)
	service.Template.Labels[names.TierLabel] = string(s.Tier)
}

func isOpen(service *run.GoogleCloudRunV2Service) bool {
	return service.InvokerIamDisabled
}

func openingOf(service *run.GoogleCloudRunV2Service) (*run.GoogleCloudRunV2Service, string) {
	if service.Labels[OpensOnPromotionLabel] == opensToEveryone {
		return &run.GoogleCloudRunV2Service{InvokerIamDisabled: true}, invokerCheckField
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

func TrafficTo(revision string, current []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
	pinned := &run.GoogleCloudRunV2TrafficTarget{Type: TrafficByRevision, Revision: revision, Percent: 100}
	traffic := []*run.GoogleCloudRunV2TrafficTarget{pinned}
	for _, target := range current {
		if target.Tag == "" {
			continue
		}
		if pinned.Tag == "" && target.Type == TrafficByRevision && RevisionName(target.Revision) == revision {
			pinned.Tag = target.Tag
			continue
		}
		traffic = append(traffic, &run.GoogleCloudRunV2TrafficTarget{Type: target.Type, Revision: target.Revision, Tag: target.Tag})
	}
	return traffic
}

type Release struct {
	Url      string
	TagURL   string
	Revision string
}

func (p *Services) OpenRun(ctx context.Context) (*gcloud.Clients, *run.Service, error) {
	clients, err := p.OpenClients(ctx)
	if err != nil {
		return nil, nil, err
	}
	services, err := clients.Run()
	return clients, services, err
}

func (p *Services) DeployService(ctx context.Context, s Serving, progress progress.Log) (Release, error) {
	if !s.Shared && s.Tier.Sibling() == "" {
		return Release{}, refusal.Refuse(refusal.CodeInvalid,
			"the Cloud Run service %s names no tier and is shared by none, and a service of a tier carries its tier's tag", s.Service)
	}
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return Release{}, err
	}
	path := clients.ServicePath(s.Service)
	desired, err := ServiceOf(s)
	if err != nil {
		return Release{}, err
	}
	version, err := p.ensureImageTag(ctx, clients, s.Image, "")
	if err != nil {
		return Release{}, err
	}
	err = p.writeRelease(ctx, clients, services, s, desired, progress)
	if isImageMissing(err, s.Image) {
		gcloud.EnsureProgress(progress).Say("Cloud Run found no image " + s.Image + ", which a prune untagged during this release: tagging it again and releasing " + s.Service + " once more")
		if _, err := p.ensureImageTag(ctx, clients, s.Image, version); err != nil {
			return Release{}, err
		}
		desired.Template.Labels[imageRetaggedLabel] = "true"
		err = p.writeRelease(ctx, clients, services, s, desired, progress)
	}
	if err != nil {
		return Release{}, err
	}
	if !s.Shared {
		if err := clients.EnsureTierTag(ctx, s.Tier, clients.RunServiceTagged(s.Service)); err != nil {
			return Release{}, err
		}
	}
	deployed, err := p.Read(ctx, services, path, s.Service)
	if err != nil {
		return Release{}, err
	}
	revision, err := p.createdRevision(ctx, services, deployed, s)
	if err != nil {
		return Release{}, err
	}
	if s.Tag == "" {
		return Release{Url: deployed.Uri, Revision: revision}, nil
	}
	if err := p.tagRevision(ctx, services, path, s.Service, revision, s.Tag); err != nil {
		return Release{}, err
	}
	tagged, err := p.Read(ctx, services, path, s.Service)
	if err != nil {
		return Release{}, err
	}
	return Release{Url: deployed.Uri, TagURL: tagURLOf(tagged, s.Tag), Revision: revision}, nil
}

func tagURLOf(current *run.GoogleCloudRunV2Service, tag string) string {
	for _, status := range current.TrafficStatuses {
		if status.Tag == tag && status.Uri != "" {
			return status.Uri
		}
	}
	if host := HostOf(current.Uri); host != "" {
		return "https://" + tag + "---" + host
	}
	return ""
}

func (p *Services) DeployAndRoute(ctx context.Context, s Serving, progress progress.Log) (Release, error) {
	ran, err := p.DeployService(ctx, s, progress)
	if err != nil {
		return Release{}, err
	}
	if _, err := p.pin(ctx, s.Service, ran.Revision, nil, false); err != nil {
		return Release{}, err
	}
	return ran, nil
}

func ServesLatest(current *run.GoogleCloudRunV2Service) bool {
	latest := RevisionName(current.LatestReadyRevision)
	return latest != "" && ServedRevision(current) == latest
}

func (p *Services) writeRelease(ctx context.Context, clients *gcloud.Clients, services *run.Service, s Serving, desired *run.GoogleCloudRunV2Service, progress progress.Log) error {
	path := clients.ServicePath(s.Service)
	_, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	switch {
	case gcloud.Absent(err):
		gcloud.EnsureProgress(progress).Say("Creating Cloud Run service " + s.Service + " in " + clients.Region)
		err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.
				Create(clients.Location(), desired).ServiceId(s.Service).Context(ctx).Do(call...)
		})
	case err != nil:
		return fmt.Errorf("read the Cloud Run service %s: %w", s.Service, err)
	default:
		gcloud.EnsureProgress(progress).Say("Releasing a new revision of Cloud Run service " + s.Service + " in " + clients.Region)
		err = p.RetryWrite(ctx, "release "+s.Service+" onto Cloud Run", func() error {
			current, err := p.Read(ctx, services, path, s.Service)
			if err != nil {
				return err
			}
			attempt := *desired
			attempt.Etag = current.Etag
			attempt.Traffic = heldTraffic(current)
			if recorded, found := current.Annotations[RollbacksAnnotation]; found {
				attempt.Annotations = map[string]string{RollbacksAnnotation: recorded}
			}
			if opened, _ := openingOf(desired); opened != nil && isOpen(current) {
				attempt.InvokerIamDisabled = opened.InvokerIamDisabled
			}
			return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
				return services.Projects.Locations.Services.Patch(path, &attempt).Context(ctx).Do(call...)
			})
		})
	}
	return err
}

func (p *Services) tagRevision(ctx context.Context, services *run.Service, path, service, revision, tag string) error {
	return p.rewriteTraffic(ctx, services, path, service, "tag revision "+revision+" of "+service+" as "+tag,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return withTag(traffic, revision, tag)
		})
}

func (p *Services) untagRevision(ctx context.Context, services *run.Service, path, service, revision string) error {
	return p.rewriteTraffic(ctx, services, path, service, "take every tag off revision "+revision+" of "+service,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return slices.DeleteFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
				return target.Percent == 0 && target.Tag != "" && RevisionName(target.Revision) == revision
			})
		})
}

func (p *Services) rewriteTraffic(
	ctx context.Context,
	services *run.Service,
	path, service, doing string,
	change func([]*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget,
) error {
	return p.RetryWrite(ctx, doing, func() error {
		current, err := p.Read(ctx, services, path, service)
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
	return []*run.GoogleCloudRunV2TrafficTarget{{Type: TrafficByLatest, Percent: 100}}
}

func withTag(traffic []*run.GoogleCloudRunV2TrafficTarget, revision, tag string) []*run.GoogleCloudRunV2TrafficTarget {
	if slices.ContainsFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Tag == tag && target.Type == TrafficByRevision && RevisionName(target.Revision) == revision
	}) {
		return traffic
	}
	return append(withoutTag(traffic, tag), &run.GoogleCloudRunV2TrafficTarget{Type: TrafficByRevision, Revision: revision, Tag: tag})
}

func sameTraffic(a, b []*run.GoogleCloudRunV2TrafficTarget) bool {
	return slices.EqualFunc(a, b, func(x, y *run.GoogleCloudRunV2TrafficTarget) bool {
		return x.Type == y.Type && RevisionName(x.Revision) == RevisionName(y.Revision) && x.Percent == y.Percent && x.Tag == y.Tag
	})
}

func heldTraffic(current *run.GoogleCloudRunV2Service) []*run.GoogleCloudRunV2TrafficTarget {
	serving := RevisionName(current.LatestReadyRevision)
	if serving == "" || len(current.Traffic) > 0 && !slices.ContainsFunc(current.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Type != TrafficByRevision
	}) {
		return current.Traffic
	}
	return TrafficTo(serving, current.Traffic)
}

func (p *Services) createdRevision(ctx context.Context, services *run.Service, current *run.GoogleCloudRunV2Service, s Serving) (string, error) {
	if current.LatestCreatedRevision == "" {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"%s created no revision, and a release routes traffic to the revision it made rather than to whatever ran last",
			s.Service)
	}
	created, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Revision, error) {
		return services.Projects.Locations.Services.Revisions.Get(current.LatestCreatedRevision).Context(ctx).Do(call...)
	})
	if err != nil {
		return "", fmt.Errorf("read the revision the release of %s created: %w", s.Service, err)
	}
	revision := RevisionName(created.Name)
	if !slices.Contains(imagesOf(created), s.Image) {
		return "", refusal.Refuse(refusal.CodeBusy,
			"the latest revision of %s, %s, runs another image than this release made it with: another release replaced it under this one, so this one routes nothing. Deploy again",
			s.Service, revision)
	}
	if !ready(created) {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"revision %s of %s did not come ready, and a release routes traffic only to a revision that can serve it",
			revision, s.Service)
	}
	return revision, nil
}

func ready(revision *run.GoogleCloudRunV2Revision) bool {
	return slices.ContainsFunc(revision.Conditions, func(condition *run.GoogleCloudRunV2Condition) bool {
		return condition.Type == ReadyCondition && condition.State == ConditionSucceeded
	})
}

func activeNamed(ctx context.Context, revision string, stillActive router.StillActive) func(*run.GoogleCloudRunV2Service) (string, error) {
	return func(*run.GoogleCloudRunV2Service) (string, error) {
		if stillActive == nil {
			return revision, nil
		}
		return revision, stillActive(ctx)
	}
}

func (p *Services) route(
	ctx context.Context,
	services *run.Service,
	path, service, doing string,
	choose func(*run.GoogleCloudRunV2Service) (string, error),
	open bool,
) (bool, error) {
	var opened bool
	err := p.RetryWrite(ctx, doing, func() error {
		opened = false
		current, err := p.Read(ctx, services, path, service)
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
		if ServedBy(current.Traffic, revision) && routed == nil && !cleared {
			return nil
		}
		if routed == nil {
			routed = &run.GoogleCloudRunV2Service{}
		}
		routed.Etag = current.Etag
		routed.Traffic = TrafficTo(revision, current.Traffic)
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
		if googleretry.IsStale(err) {
			opened = false
		}
		return err
	})
	return opened, err
}

func (p *Services) Read(ctx context.Context, services *run.Service, path, service string) (*run.GoogleCloudRunV2Service, error) {
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", service, err)
	}
	return found, nil
}

func (p *Services) RetryWrite(ctx context.Context, doing string, write func() error) error {
	var refused error
	for attempt := range ReleaseAttempts {
		if attempt > 0 && !googleretry.Waited(ctx, attempt) {
			return ctx.Err()
		}
		if refused = write(); refused == nil || !googleretry.IsStale(refused) {
			return refused
		}
	}
	return fmt.Errorf("%s, and it kept changing under this release: %w", doing, refused)
}

func ServedBy(traffic []*run.GoogleCloudRunV2TrafficTarget, revision string) bool {
	var served int64
	for _, target := range traffic {
		if target.Percent == 0 {
			continue
		}
		if target.Type != TrafficByRevision || RevisionName(target.Revision) != revision {
			return false
		}
		served += target.Percent
	}
	return served == 100
}

func RevisionName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

func (p *Services) await(ctx context.Context, services *run.Service, call func(...googleapi.CallOption) (*run.GoogleLongrunningOperation, error)) error {
	started, err := googleretry.Attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Cloud Run to release: %w", err)
	}
	finished, err := gcloud.Until(ctx, "Cloud Run to finish "+RevisionName(started.Name), func() (*run.GoogleLongrunningOperation, error) {
		if started.Done {
			return started, nil
		}
		return googleretry.Attempted(ctx, func(opt ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
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

func (p *Services) TearDown(ctx context.Context, service string, progress progress.Log) error {
	images, err := p.DeleteService(ctx, service, progress)
	if err != nil {
		return err
	}
	p.removeUnrunImages(ctx, "", images, nil, func(context.Context) (map[string]bool, error) { return nil, nil }, progress)
	return nil
}

func (p *Services) DeleteService(ctx context.Context, service string, progress progress.Log) ([]string, error) {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return nil, err
	}
	path := clients.ServicePath(service)
	images, err := p.listServiceImages(ctx, services, path)
	if err != nil && !gcloud.Absent(err) {
		gcloud.EnsureProgress(progress).Warn(fmt.Sprintf("Leaving the images %s ran tagged, as which ones could not be read: %v", service, err))
	}
	gcloud.EnsureProgress(progress).Say("Deleting Cloud Run service " + service + " in " + clients.Region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Delete(path).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return images, nil
}

func (p *Services) RemoveRevision(ctx context.Context, service, revision string, progress progress.Log) (bool, []string, error) {
	if service == "" || revision == "" {
		return false, nil, nil
	}
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return false, nil, err
	}
	path := clients.ServicePath(service)
	current, err := p.Read(ctx, services, path, service)
	if gcloud.Absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if isLatestOrRouted(current, revision) {
		return true, nil, nil
	}
	images, err := p.readRevisionImages(ctx, services, path+"/revisions/"+revision)
	if gcloud.Absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("read which image revision %s of %s runs: %w", revision, service, err)
	}
	if err := p.untagRevision(ctx, services, path, service, revision); err != nil {
		return false, nil, err
	}
	gcloud.EnsureProgress(progress).Say("Deleting revision " + revision + " of Cloud Run service " + service + " in " + clients.Region)
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Revisions.Delete(path + "/revisions/" + revision).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return false, images, nil
}

func isLatestOrRouted(service *run.GoogleCloudRunV2Service, revision string) bool {
	if RevisionName(service.LatestReadyRevision) == revision || RevisionName(service.LatestCreatedRevision) == revision {
		return true
	}
	return slices.ContainsFunc(service.Traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
		return target.Percent > 0 && RevisionName(target.Revision) == revision
	})
}

func (s Serving) billsPerRequest() bool {
	return ResolveBilling(s.Billing, s.Compute) == BillingRequest
}

type Services struct {
	OpenClients func(ctx context.Context) (*gcloud.Clients, error)
	Records     records.Store
}
