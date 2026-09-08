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

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	trafficByRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
	invokerRole       = "roles/run.invoker"
	everyone          = "allUsers"
)

const (
	revisionCPU    = "1"
	revisionMemory = "512Mi"
)

const releaseAttempts = 4

const maxRequestTimeout = 3600 * time.Second

type policyBinding = run.GoogleIamV1Binding

type serving struct {
	service string
	image   string
	env     map[string]string
	account string
	compute providerkit.Compute
	health  string
	public  bool
	memory  int
	timeout time.Duration
}

func serviceOf(s serving) (*run.GoogleCloudRunV2Service, error) {
	memory := revisionMemory
	if s.memory > 0 {
		memory = strconv.Itoa(s.memory) + "Mi"
	}
	container := &run.GoogleCloudRunV2Container{
		Image: s.image,
		Ports: []*run.GoogleCloudRunV2ContainerPort{{ContainerPort: providerkit.InjectedPort}},
		Env:   environmentOf(s.env),
		Resources: &run.GoogleCloudRunV2ResourceRequirements{
			CpuIdle:         s.compute == providerkit.ComputeServerless,
			Limits:          map[string]string{"cpu": revisionCPU, "memory": memory},
			ForceSendFields: []string{"CpuIdle"},
		},
	}
	scaling := &run.GoogleCloudRunV2RevisionScaling{ForceSendFields: []string{"MinInstanceCount"}}
	if s.compute == providerkit.ComputeContainer {
		scaling.MinInstanceCount = 1
		container.StartupProbe = &run.GoogleCloudRunV2Probe{
			HttpGet: &run.GoogleCloudRunV2HTTPGetAction{Path: s.health, Port: providerkit.InjectedPort},
		}
	}
	template := &run.GoogleCloudRunV2RevisionTemplate{
		Containers:     []*run.GoogleCloudRunV2Container{container},
		Scaling:        scaling,
		ServiceAccount: s.account,
	}
	if s.timeout > 0 {
		if s.timeout > maxRequestTimeout {
			return nil, providerkit.Refuse(providerkit.CodeInvalid,
				"%s asks for a request to run for %s, and Cloud Run cuts one off at %s: "+
					"ask for less, or move work that outlives a request off the request",
				s.service, s.timeout, maxRequestTimeout)
		}
		template.Timeout = strconv.Itoa(int(math.Ceil(s.timeout.Seconds()))) + "s"
	}
	return &run.GoogleCloudRunV2Service{Template: template}, nil
}

func environmentOf(values map[string]string) []*run.GoogleCloudRunV2EnvVar {
	carried := make([]*run.GoogleCloudRunV2EnvVar, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		carried = append(carried, &run.GoogleCloudRunV2EnvVar{Name: name, Value: values[name]})
	}
	return carried
}

func trafficTo(revision string) []*run.GoogleCloudRunV2TrafficTarget {
	return []*run.GoogleCloudRunV2TrafficTarget{
		{Type: trafficByRevision, Revision: revision, Percent: 100},
	}
}

func invokable(held []*policyBinding) ([]*policyBinding, bool) {
	for _, binding := range held {
		if binding.Role != invokerRole {
			continue
		}
		if slices.Contains(binding.Members, everyone) {
			return held, false
		}
		binding.Members = append(binding.Members, everyone)
		return held, true
	}
	return append(held, &policyBinding{Role: invokerRole, Members: []string{everyone}}), true
}

func (p *Provider) runnable(image string) string {
	repository, digest, pinned := strings.Cut(image, "@")
	if !pinned || !p.clients.emulated() {
		return image
	}
	return repository + ":" + naming.DigestTag(digest)
}

func (p *Provider) location() string {
	return "projects/" + p.options.Project + "/locations/" + p.options.Region
}

func (p *Provider) servicePath(service string) string {
	return p.location() + "/services/" + service
}

func (p *Provider) stand(ctx context.Context, s serving, report providerkit.Reporter) (string, error) {
	services, err := p.clients.Run()
	if err != nil {
		return "", err
	}
	path := p.servicePath(s.service)
	s.image = p.runnable(s.image)
	desired, err := serviceOf(s)
	if err != nil {
		return "", err
	}

	_, err = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	switch {
	case absent(err):
		if report != nil {
			report.Say("Standing " + s.service + " up on Cloud Run")
		}
		err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.
				Create(p.location(), desired).ServiceId(s.service).Context(ctx).Do(call...)
		})
	case err != nil:
		return "", fmt.Errorf("read the Cloud Run service %s: %w", s.service, err)
	default:
		if report != nil {
			report.Say("Releasing " + s.service + " onto Cloud Run")
		}
		err = p.settled(ctx, "release "+s.service+" onto Cloud Run", func() error {
			held, err := p.read(ctx, services, path, s.service)
			if err != nil {
				return err
			}
			desired.Etag = held.Etag
			desired.Traffic = held.Traffic
			return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
				return services.Projects.Locations.Services.Patch(path, desired).Context(ctx).Do(call...)
			})
		})
	}
	if err != nil {
		return "", err
	}
	stood, err := p.pin(ctx, services, path, s.service)
	if err != nil {
		return "", err
	}
	if s.public {
		if err := p.open(ctx, services, path, s.service); err != nil {
			return "", err
		}
	}
	return stood.Uri, nil
}

func (p *Provider) pin(ctx context.Context, services *run.Service, path, service string) (*run.GoogleCloudRunV2Service, error) {
	var stood *run.GoogleCloudRunV2Service
	err := p.settled(ctx, "pin the traffic of "+service+" to the revision this release stood up", func() error {
		held, err := p.read(ctx, services, path, service)
		if err != nil {
			return err
		}
		stood = held
		revision := revisionName(held.LatestReadyRevision)
		if revision == "" {
			return providerkit.Refuse(providerkit.CodeNotReady,
				"%s stood up no revision that came ready, and a release routes traffic to the revision it made rather than to whatever ran last",
				service)
		}
		if servedBy(held.Traffic, revision) {
			return nil
		}
		routed := &run.GoogleCloudRunV2Service{
			Etag:     held.Etag,
			Template: held.Template,
			Traffic:  trafficTo(revision),
		}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, routed).Context(ctx).Do(call...)
		})
	})
	if err != nil {
		return nil, err
	}
	return stood, nil
}

func (p *Provider) read(ctx context.Context, services *run.Service, path, service string) (*run.GoogleCloudRunV2Service, error) {
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", service, err)
	}
	return held, nil
}

func (p *Provider) settled(ctx context.Context, doing string, write func() error) error {
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
	return false
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

func (p *Provider) open(ctx context.Context, services *run.Service, path, service string) error {
	return p.settled(ctx, "open "+service+" to the internet", func() error {
		policy, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.GetIamPolicy(path).Context(ctx).Do(call...)
		})
		if err != nil {
			return deniedPolicy(service, err)
		}
		bound, changed := invokable(policy.Bindings)
		if !changed {
			return nil
		}
		policy.Bindings = bound
		_, err = attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.
				SetIamPolicy(path, &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(call...)
		})
		if err == nil || stale(err) {
			return err
		}
		return deniedPolicy(service, err)
	})
}

func deniedPolicy(service string, err error) error {
	if answeredCode(err) != http.StatusForbidden {
		return fmt.Errorf("open %s to the internet: %w", service, err)
	}
	return providerkit.Refuse(providerkit.CodeDenied,
		"binding %s to %s on the Cloud Run service %s was refused, and a service nobody may invoke answers nothing: "+
			"grant the deploying principal %s, and check that %s does not deny %s in this organization.\n%v",
		everyone, invokerRole, service, "roles/run.admin", "constraints/iam.allowedPolicyMemberDomains", everyone, err)
}

func (p *Provider) await(ctx context.Context, services *run.Service, call func(...googleapi.CallOption) (*run.GoogleLongrunningOperation, error)) error {
	started, err := attempted(ctx, call)
	if err != nil {
		return fmt.Errorf("ask Cloud Run to release: %w", err)
	}
	settled, err := until(ctx, "Cloud Run to finish "+revisionName(started.Name), func() (*run.GoogleLongrunningOperation, error) {
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
	if settled.Error != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"Cloud Run refused the release: %s", settled.Error.Message)
	}
	return nil
}

func (p *Provider) tearDown(ctx context.Context, service string, report providerkit.Reporter) error {
	services, err := p.clients.Run()
	if err != nil {
		return err
	}
	if report != nil {
		report.Say("Taking " + service + " down")
	}
	err = p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
		return services.Projects.Locations.Services.Delete(p.servicePath(service)).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil
	}
	return err
}
