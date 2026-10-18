package run

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func (p *Services) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error) {
	return p.pin(ctx, service, revision, stillActive, true)
}

func (p *Services) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	_, err := p.pin(ctx, service, revision, stillActive, false)
	return err
}

func (p *Services) pin(ctx context.Context, service, revision string, stillActive router.StillActive, open bool) (bool, error) {
	if revision == "" {
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s is asked to serve a revision nothing named, and traffic is pinned to one revision by name", service)
	}
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return false, err
	}
	return p.route(ctx, services, clients.ServicePath(service), service,
		"pin the traffic of "+service+" to "+revision, activeNamed(ctx, revision, stillActive), open)
}

func (p *Services) Close(ctx context.Context, service string, stillActive router.StillActive) error {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return err
	}
	path := clients.ServicePath(service)
	err = p.RetryWrite(ctx, "close "+service+" to everyone but its invokers", func() error {
		current, err := p.Read(ctx, services, path, service)
		if err != nil {
			return err
		}
		if !isOpen(current) {
			return nil
		}
		if stillActive != nil {
			if err := stillActive(ctx); err != nil {
				return err
			}
		}
		closed := &run.GoogleCloudRunV2Service{Etag: current.Etag, ForceSendFields: []string{"InvokerIamDisabled"}}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, closed).UpdateMask(invokerCheckField).Context(ctx).Do(call...)
		})
	})
	if gcloud.Absent(err) {
		return nil
	}
	return err
}

const (
	RollbacksAnnotation = "ocel.dev/rollbacks"
	annotationsField    = "annotations"
	maxRollbacks        = 32
)

func (p *Services) ReadRollback(ctx context.Context, service, revision string) (pin.Rollback, bool, error) {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return pin.Rollback{}, false, err
	}
	current, err := p.Read(ctx, services, clients.ServicePath(service), service)
	if gcloud.Absent(err) {
		return pin.Rollback{}, false, nil
	}
	if err != nil {
		return pin.Rollback{}, false, err
	}
	rollbacks, err := rollbacksOf(current)
	if err != nil {
		return pin.Rollback{}, false, err
	}
	rollback, found := rollbacks[revision]
	return rollback, found, nil
}

func (p *Services) RecordRollback(ctx context.Context, service, revision string, rollback pin.Rollback) error {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return err
	}
	path := clients.ServicePath(service)
	err = p.RetryWrite(ctx, "record on "+service+" that the pin of "+revision+" was rolled back", func() error {
		current, err := p.Read(ctx, services, path, service)
		if err != nil {
			return err
		}
		if ServedRevision(current) == revision {
			return pin.ErrRevisionServed
		}
		rollbacks, err := rollbacksOf(current)
		if err != nil {
			rollbacks = map[string]pin.Rollback{}
		}
		rollbacks[revision] = rollback
		annotations, err := withRollbacks(current.Annotations, rollbacks)
		if err != nil {
			return err
		}
		recorded := &run.GoogleCloudRunV2Service{Etag: current.Etag, Annotations: annotations, ForceSendFields: []string{"Annotations"}}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, recorded).UpdateMask(annotationsField).Context(ctx).Do(call...)
		})
	})
	if gcloud.Absent(err) {
		return nil
	}
	return err
}

func rollbacksOf(current *run.GoogleCloudRunV2Service) (map[string]pin.Rollback, error) {
	rollbacks := map[string]pin.Rollback{}
	if recorded := current.Annotations[RollbacksAnnotation]; recorded != "" {
		if err := json.Unmarshal([]byte(recorded), &rollbacks); err != nil {
			return nil, fmt.Errorf("decode the rollbacks recorded on %s: %w", current.Name, err)
		}
	}
	return rollbacks, nil
}

func withRollbacks(annotations map[string]string, rollbacks map[string]pin.Rollback) (map[string]string, error) {
	kept := maps.Clone(annotations)
	if kept == nil {
		kept = map[string]string{}
	}
	if len(rollbacks) == 0 {
		delete(kept, RollbacksAnnotation)
		return kept, nil
	}
	for _, revision := range slices.Sorted(maps.Keys(rollbacks)) {
		if len(rollbacks) <= maxRollbacks {
			break
		}
		delete(rollbacks, revision)
	}
	encoded, err := json.Marshal(rollbacks)
	if err != nil {
		return nil, err
	}
	kept[RollbacksAnnotation] = string(encoded)
	return kept, nil
}

func withoutRollback(current *run.GoogleCloudRunV2Service, revision string) (map[string]string, bool, error) {
	rollbacks, err := rollbacksOf(current)
	if err != nil {
		annotations, err := withRollbacks(current.Annotations, map[string]pin.Rollback{})
		return annotations, true, err
	}
	if _, recorded := rollbacks[revision]; !recorded {
		return nil, false, nil
	}
	delete(rollbacks, revision)
	annotations, err := withRollbacks(current.Annotations, rollbacks)
	return annotations, true, err
}

const WarmTimeout = 20 * time.Second

var warmClient = &http.Client{Timeout: WarmTimeout}

func (p *Services) Warm(ctx context.Context, service, revision, path string) error {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return err
	}
	current, err := p.Read(ctx, services, clients.ServicePath(service), service)
	if err != nil {
		return err
	}
	address := warmAddress(current, revision)
	if address == "" {
		return nil
	}
	address = strings.TrimSuffix(address, "/")
	asked, err := http.NewRequestWithContext(ctx, http.MethodHead, address+path, nil)
	if err != nil {
		return err
	}
	answered, err := warmClient.Do(asked)
	if err != nil {
		return fmt.Errorf("warm revision %s of %s: %w", revision, service, err)
	}
	defer answered.Body.Close()
	if answeredByGate(answered) {
		return nil
	}
	if answered.StatusCode == http.StatusUnauthorized || answered.StatusCode == http.StatusForbidden || answered.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("warm revision %s of %s: it answered %s", revision, service, answered.Status)
	}
	return nil
}

func answeredByGate(answered *http.Response) bool {
	return answered.StatusCode == http.StatusUnauthorized &&
		answered.Header.Get("Cache-Control") == gate.CacheControl &&
		answered.Header.Get("X-Robots-Tag") == gate.RobotsTag
}

func warmAddress(current *run.GoogleCloudRunV2Service, revision string) string {
	if current.Ingress != IngressEverywhere || !isOpen(current) {
		return ""
	}
	for _, status := range current.TrafficStatuses {
		if status.Tag != "" && status.Uri != "" && RevisionName(status.Revision) == revision {
			return status.Uri
		}
	}
	if ServedBy(current.Traffic, revision) {
		return current.Uri
	}
	return ""
}

func (p *Services) ReadServing(ctx context.Context, service string) (string, error) {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return "", err
	}
	current, err := p.Read(ctx, services, clients.ServicePath(service), service)
	if gcloud.Absent(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ServedRevision(current), nil
}

func ServedRevision(current *run.GoogleCloudRunV2Service) string {
	var serving string
	for _, target := range allocatedTraffic(current) {
		if target.Percent == 0 {
			continue
		}
		revision := RevisionName(target.Revision)
		if target.Type == TrafficByLatest {
			revision = RevisionName(current.LatestReadyRevision)
		}
		if serving != "" && serving != revision {
			return ""
		}
		serving = revision
	}
	return serving
}

func (p *Services) ReadTag(ctx context.Context, service, revision string) (string, error) {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return "", err
	}
	current, err := p.Read(ctx, services, clients.ServicePath(service), service)
	if err != nil {
		return "", err
	}
	for _, target := range current.Traffic {
		if target.Tag != "" && target.Type == TrafficByRevision && RevisionName(target.Revision) == revision {
			return target.Tag, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeNotReady,
		"revision %s of Cloud Run service %s carries no tag, and a deployment hostname reaches its revision through the tag its release gave it: "+
			"re-deploy so the release tags the revision it creates", revision, service)
}

func (p *Services) Untag(ctx context.Context, service, tag string) error {
	clients, services, err := p.OpenRun(ctx)
	if err != nil {
		return err
	}
	err = p.rewriteTraffic(ctx, services, clients.ServicePath(service), service, "take tag "+tag+" off "+service,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return withoutTag(traffic, tag)
		})
	if gcloud.Absent(err) {
		return nil
	}
	return err
}

func withoutTag(traffic []*run.GoogleCloudRunV2TrafficTarget, tag string) []*run.GoogleCloudRunV2TrafficTarget {
	kept := make([]*run.GoogleCloudRunV2TrafficTarget, 0, len(traffic))
	for _, target := range traffic {
		switch {
		case target.Tag != tag:
			kept = append(kept, target)
		case target.Percent > 0:
			untagged := *target
			untagged.Tag = ""
			kept = append(kept, &untagged)
		}
	}
	return kept
}
