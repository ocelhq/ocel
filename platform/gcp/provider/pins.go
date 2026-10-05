package gcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/iamcredentials/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func (p *Provider) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error) {
	return p.pin(ctx, service, revision, stillActive, true)
}

func (p *Provider) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	_, err := p.pin(ctx, service, revision, stillActive, false)
	return err
}

func (p *Provider) pin(ctx context.Context, service, revision string, stillActive router.StillActive, open bool) (bool, error) {
	if revision == "" {
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s is asked to serve a revision nothing named, and traffic is pinned to one revision by name", service)
	}
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return false, err
	}
	return p.route(ctx, services, clients.servicePath(service), service,
		"pin the traffic of "+service+" to "+revision, activeNamed(ctx, revision, stillActive), open)
}

func (p *Provider) Close(ctx context.Context, service string, stillActive router.StillActive) error {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	path := clients.servicePath(service)
	err = p.retryWrite(ctx, "close "+service+" to everyone but its invokers", func() error {
		current, err := p.read(ctx, services, path, service)
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
		closed := &run.GoogleCloudRunV2Service{Etag: current.Etag, ForceSendFields: []string{"InvokerIamDisabled", "IapEnabled"}}
		return p.await(ctx, services, func(call ...googleapi.CallOption) (*run.GoogleLongrunningOperation, error) {
			return services.Projects.Locations.Services.Patch(path, closed).UpdateMask(invokerCheckField + "," + proxyField).Context(ctx).Do(call...)
		})
	})
	if absent(err) {
		return nil
	}
	return err
}

const (
	rollbacksAnnotation = "ocel.dev/rollbacks"
	annotationsField    = "annotations"
	maxRollbacks        = 32
)

func (p *Provider) ReadRollback(ctx context.Context, service, revision string) (pin.Rollback, bool, error) {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return pin.Rollback{}, false, err
	}
	current, err := p.read(ctx, services, clients.servicePath(service), service)
	if absent(err) {
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

func (p *Provider) RecordRollback(ctx context.Context, service, revision string, rollback pin.Rollback) error {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	path := clients.servicePath(service)
	err = p.retryWrite(ctx, "record on "+service+" that the pin of "+revision+" was rolled back", func() error {
		current, err := p.read(ctx, services, path, service)
		if err != nil {
			return err
		}
		if servedRevision(current) == revision {
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
	if absent(err) {
		return nil
	}
	return err
}

func rollbacksOf(current *run.GoogleCloudRunV2Service) (map[string]pin.Rollback, error) {
	rollbacks := map[string]pin.Rollback{}
	if recorded := current.Annotations[rollbacksAnnotation]; recorded != "" {
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
		delete(kept, rollbacksAnnotation)
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
	kept[rollbacksAnnotation] = string(encoded)
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

const (
	warmTimeout       = 20 * time.Second
	warmTokenLifetime = 5 * time.Minute
	httpsPort         = "443"
)

var warmClient = &http.Client{Timeout: warmTimeout}

var warmRoots *x509.CertPool

func (p *Provider) Warm(ctx context.Context, service, revision, path string) error {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	current, err := p.read(ctx, services, clients.servicePath(service), service)
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
	if current.IapEnabled {
		token, err := p.signProxyToken(ctx, clients, runsAs(current), address)
		if err != nil {
			return fmt.Errorf("warm revision %s of %s: %w", revision, service, err)
		}
		asked.Header.Set("Authorization", "Bearer "+token)
	}
	answered, err := warmClient.Do(asked)
	if err != nil {
		return fmt.Errorf("warm revision %s of %s: %w", revision, service, err)
	}
	defer answered.Body.Close()
	if answered.StatusCode == http.StatusUnauthorized || answered.StatusCode == http.StatusForbidden || answered.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("warm revision %s of %s: it answered %s", revision, service, answered.Status)
	}
	return nil
}

func (p *Provider) signProxyToken(ctx context.Context, c *clients, account, address string) (string, error) {
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": account,
		"sub": account,
		"aud": address + "/*",
		"iat": now.Unix(),
		"exp": now.Add(warmTokenLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	signing, err := c.IAMCredentials()
	if err != nil {
		return "", err
	}
	signed, err := attempted(ctx, func(call ...googleapi.CallOption) (*iamcredentials.SignJwtResponse, error) {
		return signing.Projects.ServiceAccounts.SignJwt("projects/-/serviceAccounts/"+account,
			&iamcredentials.SignJwtRequest{Payload: string(claims)}).Context(ctx).Do(call...)
	})
	if answeredCode(err) == http.StatusForbidden {
		return "", fmt.Errorf("the warm passes Identity-Aware Proxy with a token signed as %s, which this credential may not sign: "+
			"the deploy grants its own credential that on the account, so promote with the credential that deployed", account)
	}
	if err != nil {
		return "", fmt.Errorf("sign a token Identity-Aware Proxy admits as %s: %w", account, err)
	}
	return signed.SignedJwt, nil
}

func warmThrough(ctx context.Context, url, address string) error {
	asked, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: warmRoots}}
	if address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(address, httpsPort)
		}
		var dialer net.Dialer
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}
	}
	defer transport.CloseIdleConnections()
	answered, err := (&http.Client{Timeout: warmTimeout, Transport: transport}).Do(asked)
	if err != nil {
		return fmt.Errorf("warm %s: %w", url, err)
	}
	return answered.Body.Close()
}

func warmAddress(current *run.GoogleCloudRunV2Service, revision string) string {
	if current.Ingress != ingressEverywhere || !isOpen(current) {
		return ""
	}
	for _, status := range current.TrafficStatuses {
		if status.Tag != "" && status.Uri != "" && revisionName(status.Revision) == revision {
			return status.Uri
		}
	}
	if servedBy(current.Traffic, revision) {
		return current.Uri
	}
	return ""
}

func (p *Provider) ReadServing(ctx context.Context, service string) (string, error) {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return "", err
	}
	current, err := p.read(ctx, services, clients.servicePath(service), service)
	if absent(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return servedRevision(current), nil
}

func servedRevision(current *run.GoogleCloudRunV2Service) string {
	var serving string
	for _, target := range allocatedTraffic(current) {
		if target.Percent == 0 {
			continue
		}
		revision := revisionName(target.Revision)
		if target.Type == trafficByLatest {
			revision = revisionName(current.LatestReadyRevision)
		}
		if serving != "" && serving != revision {
			return ""
		}
		serving = revision
	}
	return serving
}

func (p *Provider) ReadTag(ctx context.Context, service, revision string) (string, error) {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return "", err
	}
	current, err := p.read(ctx, services, clients.servicePath(service), service)
	if err != nil {
		return "", err
	}
	for _, target := range current.Traffic {
		if target.Tag != "" && target.Type == trafficByRevision && revisionName(target.Revision) == revision {
			return target.Tag, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeNotReady,
		"revision %s of Cloud Run service %s carries no tag, and a deployment hostname reaches its revision through the tag its release gave it: "+
			"re-deploy so the release tags the revision it creates", revision, service)
}

func (p *Provider) Untag(ctx context.Context, service, tag string) error {
	clients, services, err := p.openRun(ctx)
	if err != nil {
		return err
	}
	err = p.rewriteTraffic(ctx, services, clients.servicePath(service), service, "take tag "+tag+" off "+service,
		func(traffic []*run.GoogleCloudRunV2TrafficTarget) []*run.GoogleCloudRunV2TrafficTarget {
			return withoutTag(traffic, tag)
		})
	if absent(err) {
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

func runsAs(service *run.GoogleCloudRunV2Service) string {
	if service.Template == nil {
		return ""
	}
	return service.Template.ServiceAccount
}
