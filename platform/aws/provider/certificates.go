package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Certificate(ctx context.Context, kind edge.Kind, hostname string, report providerkit.Reporter) (string, error) {
	registry := p.edges()
	front, err := registry.Open(kind)
	if err != nil {
		return "", err
	}
	certifier := registry.Certifier(front, certs.Deps{AWS: p.aws})
	if note := edges.IgnoredPinNote(front, certifier, hostname); note != "" {
		report.Detail(note)
	}
	if !certifier.Issues() {
		return "", nil
	}
	pinned := certifier.PinFor(hostname)
	if pinned == "" {
		// TODO(#390): nothing here requests a certificate yet, so a hostname without a pin
		// has none to be served with; refuse before the edge attaches an empty one.
		return "", providerkit.Refuse(providerkit.CodeNotReady,
			"the %s edge terminates TLS for %s itself, and ocel issues no certificate yet: pin one already issued in %s that covers %s under `certificates` in this provider's options, then run this again",
			front.Kind(), hostname, certifier.Issuer.Region, hostname)
	}
	held, err := certifier.Issuer.Pinned(ctx, hostname, pinned)
	if err != nil {
		return "", providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return held.ARN, nil
}
