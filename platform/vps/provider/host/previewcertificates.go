package host

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const PerHostnamePreviewCertificatesOption = "perHostnamePreviewCertificates"

func (h *Host) RefusePerHostnamePreviewCertificates(ctx context.Context, destination string) error {
	if !h.front.Guarantees().OrdersEachPreviewHostnameItsOwnCertificate {
		return nil
	}
	state, err := h.routingTable(ctx)
	if err != nil {
		return err
	}
	base := destination
	if base == "" {
		base = state.PreviewBase
	}
	if base == "" || caddy.IsInternal(base) {
		return nil
	}
	previewed := edge.ProbeHostname(edge.PreviewWildcard(base))
	if Covering(h.pins, previewed) != "" {
		return nil
	}
	if shield, shielded := proxySpec(state).ShieldOf(previewed); shielded && shield.OriginCertificate.Certificate != "" {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s on %s orders a publicly trusted certificate for each preview hostname under %s, and Certificate Transparency logs publish every one of them within minutes, so anyone can find this preview\n"+
			"%s, or front this box with an edge that presents its own certificate and shields the box with an origin certificate, or set the option %q to true to accept that its hostnames are published",
		h.proxyOption.named(), h.named(), edge.PreviewWildcard(base), h.proxyOption.describeSharedPreviewCertificate(base), PerHostnamePreviewCertificatesOption)
}

func (f Front) describeSharedPreviewCertificate(base string) string {
	switch {
	case f.Traefik != nil:
		return "Set the option \"proxy.traefik.previewResolver\" to a resolver that issues " + edge.PreviewWildcard(base) + " over DNS-01, so previews share one wildcard certificate"
	case f.Caddy != nil:
		return "Serve " + edge.PreviewWildcard(base) + " from a wildcard certificate in your Caddy"
	default:
		return "Pin a certificate for " + edge.PreviewWildcard(base) + " under the option \"certificates\", so previews share it"
	}
}
