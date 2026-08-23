package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	gateProbeAttempts = 3
	gateProbeEvery    = 5 * time.Second
	gateProbeAtOnce   = 8
)

type certLookup struct {
	certifier certs.Certifier
	recorded  domains.Settlement
	seen      map[string]certs.Certificate
}

func newCertLookup(certifier certs.Certifier, recorded domains.Settlement) *certLookup {
	return &certLookup{certifier: certifier, recorded: recorded, seen: map[string]certs.Certificate{}}
}

func (l *certLookup) of(ctx context.Context, host string) (certs.Certificate, error) {
	if !l.certifier.Issues() {
		return certs.Certificate{}, nil
	}
	arn := l.certifier.Wants(certs.Certificate{ARN: l.recorded.Host(host).Certificate}, host)
	if arn == "" {
		return certs.Certificate{}, nil
	}
	if region := certs.RegionOfARN(arn); region != "" && region != l.certifier.Issuer.Region {
		return certs.Certificate{}, fmt.Errorf("the certificate for %s lives in %s, but this edge terminates TLS in %s: run `ocel domain add` to settle one there", host, region, l.certifier.Issuer.Region)
	}
	if cert, ok := l.seen[arn]; ok {
		return cert, nil
	}
	cert, err := l.certifier.Issuer.Describe(ctx, certs.Certificate{ARN: arn, Region: l.certifier.Issuer.Region})
	if err != nil {
		return certs.Certificate{}, fmt.Errorf("could not read the certificate %s bound to %s from ACM in %s: %w — check it still exists and that these credentials may describe it", arn, host, l.certifier.Issuer.Region, err)
	}
	if cert.Region == "" {
		cert.Region = l.certifier.Issuer.Region
	}
	l.seen[arn] = cert
	return cert, nil
}

type domainGate struct {
	kind          edge.Kind
	servesUnbound bool

	state     edge.StackState
	recorded  domains.Settlement
	certifier certs.Certifier
	prober    certs.Prober
	previewOn string
	now       func() time.Time
}

type admission struct {
	withheldURLs string
}

func (g domainGate) clock() time.Time {
	if g.now == nil {
		return time.Now()
	}
	return g.now()
}

func (g domainGate) admitPreview(manifest *contractv1.Manifest) error {
	if len(deploy.DeclaredHostnames(manifest, environmentv1.Tier_TIER_PREVIEW)) > 0 || g.previewOn != "" {
		return nil
	}
	return fmt.Errorf(
		"this project declares no domains.preview wildcard and no global preview domain is in use, so a preview deploy has nowhere to serve: declare a project-level domains.preview wildcard, or run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard",
	)
}

func (g domainGate) admitProduction(ctx context.Context, manifest *contractv1.Manifest, warn func(string)) (admission, error) {
	hosts := deploy.DeclaredHostnames(manifest, environmentv1.Tier_TIER_PRODUCTION)
	if len(hosts) == 0 {
		return admission{}, fmt.Errorf(
			"this project declares no domains.production, so a production deploy has no hostname to serve: declare one in your ocel config and run `ocel domain add` to provision the certificate, the edge surface and the DNS for it",
		)
	}
	if g.state.Empty() {
		return admission{withheldURLs: fmt.Sprintf(
			"this deploy is the one that creates the edge surface, so nothing is bound to %s yet: run `ocel domain add` to settle the certificate, the surface and the DNS, then deploy again — until then there is no address of yours to print",
			strings.Join(hosts, ", "),
		)}, nil
	}
	bound := g.state.Bound
	lookup := newCertLookup(g.certifier, g.recorded)
	for _, host := range hosts {
		cert, err := lookup.of(ctx, host)
		if err != nil {
			return admission{}, err
		}
		if err := g.admitHost(host, cert, bound); err != nil {
			return admission{}, err
		}
		if cert.ExpiringSoon(g.clock()) {
			warn(fmt.Sprintf(
				"certificate %s covering %s expires %s and ACM has not renewed it (renewal is %s): check the validation record is still published, or run `ocel domain add` to settle a fresh certificate",
				cert.ARN, host, cert.NotAfter.UTC().Format(time.RFC3339), renewalWord(cert.Renewal),
			))
		}
	}
	return admission{}, g.probeAll(ctx, hosts)
}

func (g domainGate) admitHost(host string, cert certs.Certificate, bound []string) error {
	if g.certifier.Issues() {
		switch {
		case cert.ARN == "":
			return fmt.Errorf("no certificate covers %s: run `ocel domain add` to request one before deploying to it", host)
		case !cert.Issued():
			return fmt.Errorf("the certificate for %s is %s, not %s: run `ocel domain add` and let it finish before deploying to it", host, certStatusWord(cert.Status), strings.ToLower(certs.StatusIssued))
		case !cert.Covers(host):
			return fmt.Errorf("the certificate for %s covers %s, which does not include it: run `ocel domain add` to settle one that does", host, strings.Join(cert.Domains, ", "))
		}
	}
	if !slices.Contains(bound, host) {
		return fmt.Errorf("%s is not bound to the %s edge, so nothing there would answer for it: run `ocel domain add`", host, g.kind)
	}
	return nil
}

func (g domainGate) probeAll(ctx context.Context, hosts []string) error {
	fresh := g.prober
	fresh.Attempts = min(fresh.Attempts, gateProbeAttempts)
	if fresh.Every <= 0 || fresh.Every > gateProbeEvery {
		fresh.Every = gateProbeEvery
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	failures := make([]error, len(hosts))
	at := make(chan struct{}, gateProbeAtOnce)
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		at <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-at }()
			if _, err := fresh.Await(ctx, host, g.kind, g.recorded.Host(host).Records.Owed, func(string) {}); err != nil {
				failures[i] = fmt.Errorf("%s does not answer as the %s edge yet, so its DNS record has not taken: %w — run `ocel domain status --wait` to watch it, or `ocel domain add` to settle what is missing", host, g.kind, err)
			}
		}()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			return err
		}
	}
	return nil
}

func renewalWord(status string) string {
	if status == "" {
		return "not reported"
	}
	return strings.ToLower(status)
}

func admitDomains(ctx context.Context, gate domainGate, tier environmentv1.Tier, manifest *contractv1.Manifest, warn func(string)) (admission, error) {
	switch tier {
	case environmentv1.Tier_TIER_PREVIEW:
		if err := gate.admitPreview(manifest); err != nil {
			return admission{}, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return admission{}, nil
	case environmentv1.Tier_TIER_PRODUCTION:
		admitted, err := gate.admitProduction(ctx, manifest, warn)
		if err != nil {
			return admission{}, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return admitted, nil
	default:
		return admission{}, nil
	}
}
