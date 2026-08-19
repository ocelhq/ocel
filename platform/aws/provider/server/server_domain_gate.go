package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	gateProbeAttempts = 3
	gateProbeEvery    = 5 * time.Second
	gateProbeAtOnce   = 8
)

type certLookup struct {
	issuer   certs.Issuer
	recorded bootstrap.Production
	pins     map[string]string
	seen     map[string]certs.Certificate
}

func newCertLookup(issuer certs.Issuer, recorded bootstrap.Production, pins map[string]string) *certLookup {
	return &certLookup{issuer: issuer, recorded: recorded, pins: pins, seen: map[string]certs.Certificate{}}
}

func (l *certLookup) ignoredPin(host string) bool {
	return l.issuer.API == nil && l.pins[host] != ""
}

func (l *certLookup) of(ctx context.Context, host string) (certs.Certificate, error) {
	if l.issuer.API == nil {
		return certs.Certificate{}, nil
	}
	arn := l.recorded.Host(host).Certificate
	if pinned := l.pins[host]; pinned != "" {
		arn = pinned
	}
	if arn == "" {
		return certs.Certificate{}, nil
	}
	if region := certs.RegionOfARN(arn); region != "" && region != l.issuer.Region {
		return certs.Certificate{}, fmt.Errorf("the certificate for %s lives in %s, but this edge terminates TLS in %s: run `ocel domain add` to settle one there", host, region, l.issuer.Region)
	}
	if cert, ok := l.seen[arn]; ok {
		return cert, nil
	}
	cert, err := l.issuer.Describe(ctx, certs.Certificate{ARN: arn, Region: l.issuer.Region})
	if err != nil {
		return certs.Certificate{}, fmt.Errorf("could not read the certificate %s bound to %s from ACM in %s: %w — check it still exists and that these credentials may describe it", arn, host, l.issuer.Region, err)
	}
	if cert.Region == "" {
		cert.Region = l.issuer.Region
	}
	l.seen[arn] = cert
	return cert, nil
}

type domainGate struct {
	kind          edge.Kind
	servesUnbound bool

	state     edge.StackState
	recorded  bootstrap.Production
	issuer    certs.Issuer
	prober    certs.Prober
	pins      map[string]string
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

func (g domainGate) admitPreview(manifest *deploymentsv1.Manifest) error {
	if len(deploy.DeclaredHostnames(manifest, deploymentsv1.Environment_CLASS_PREVIEW)) > 0 || g.previewOn != "" {
		return nil
	}
	return fmt.Errorf(
		"this project declares no domains.preview wildcard and no global preview domain is in use, so a preview deploy has nowhere to serve: declare a project-level domains.preview wildcard, or run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard",
	)
}

func (g domainGate) admitProduction(ctx context.Context, manifest *deploymentsv1.Manifest, warn func(string)) (admission, error) {
	hosts := deploy.DeclaredHostnames(manifest, deploymentsv1.Environment_CLASS_PRODUCTION)
	if len(hosts) == 0 {
		return admission{}, fmt.Errorf(
			"this project declares no domains.production, so a production deploy has no hostname to serve: declare one in your ocel config and run `ocel domain add` to provision the certificate, the edge surface and the DNS for it",
		)
	}
	if len(g.state) == 0 {
		return admission{withheldURLs: fmt.Sprintf(
			"this deploy is the one that creates the edge surface, so nothing is bound to %s yet: run `ocel domain add` to settle the certificate, the surface and the DNS, then deploy again — until then there is no address of yours to print",
			strings.Join(hosts, ", "),
		)}, nil
	}
	bound := edge.BoundDomains(g.state)
	lookup := newCertLookup(g.issuer, g.recorded, g.pins)
	for _, host := range hosts {
		cert, err := lookup.of(ctx, host)
		if err != nil {
			return admission{}, err
		}
		if err := g.admitHost(host, cert, bound); err != nil {
			return admission{}, err
		}
		if lookup.ignoredPin(host) {
			warn(fmt.Sprintf(
				"the certificate pinned for %s is ignored: the %s edge terminates TLS with a certificate of its own, so ocel neither requests nor uses one here",
				host, g.kind,
			))
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
	if g.issuer.API != nil {
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
			if _, err := fresh.Await(ctx, host, g.kind, g.recorded.Host(host).Owed, func(string) {}); err != nil {
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

func admitDomains(ctx context.Context, gate domainGate, class deploymentsv1.Environment_Class, manifest *deploymentsv1.Manifest, warn func(string)) (admission, error) {
	switch class {
	case deploymentsv1.Environment_CLASS_PREVIEW:
		if err := gate.admitPreview(manifest); err != nil {
			return admission{}, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return admission{}, nil
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		admitted, err := gate.admitProduction(ctx, manifest, warn)
		if err != nil {
			return admission{}, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return admitted, nil
	default:
		return admission{}, nil
	}
}
