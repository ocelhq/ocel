package host

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

const PerHostnamePreviewCertificatesOption = "perHostnamePreviewCertificates"

func (h *Host) RefusePerHostnamePreviewCertificates(ctx context.Context, destination string) error {
	if !h.front.Guarantees().OrdersEachPreviewHostnameItsOwnCertificate {
		return nil
	}
	state, err := h.routing.Read(ctx)
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
	if h.pins.Covering(previewed) != "" {
		return nil
	}
	if shield, shielded := routing.Spec(state).ShieldOf(previewed); shielded && shield.OriginCertificate.Certificate != "" {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s on %s orders a publicly trusted certificate for each preview hostname under %s, and Certificate Transparency logs publish every one of them within minutes, so anyone can find this preview\n"+
			"%s, or front this box with an edge that presents its own certificate and shields the box with an origin certificate, or set the option %q to true to accept that its hostnames are published",
		h.proxyOption.Front().Named(), h.Named(), edge.PreviewWildcard(base), h.proxyOption.Front().DescribeSharedPreviewCertificate(base), PerHostnamePreviewCertificatesOption)
}
