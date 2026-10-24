package routing

import (
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

const (
	DeployWindow = 60 * time.Second
	DrainWindow  = 30 * time.Second
)

func MergeShield(shields []boxapi.Shield, merged boxapi.Shield) []boxapi.Shield {
	merged.ClientCAs = slices.Compact(slices.Sorted(slices.Values(merged.ClientCAs)))
	kept := make([]boxapi.Shield, 0, len(shields)+1)
	for _, shield := range shields {
		if shield.Hostname == merged.Hostname {
			if merged.OriginCertificate.Certificate == "" && shield.Owner == merged.Owner {
				merged.OriginCertificate = shield.OriginCertificate
			}
			continue
		}
		if slices.ContainsFunc(shield.ClientCAs, func(certificate string) bool { return slices.Contains(merged.ClientCAs, certificate) }) {
			shield.ClientCAs = slices.Compact(slices.Sorted(slices.Values(slices.Concat(shield.ClientCAs, merged.ClientCAs))))
		}
		kept = append(kept, shield)
	}
	return append(kept, merged)
}

func DropShields(shields []boxapi.Shield, dropped func(boxapi.Shield) bool) []boxapi.Shield {
	return slices.DeleteFunc(slices.Clone(shields), dropped)
}

func RefuseInvalidPreviewBase(base string) error {
	if base == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"the preview entry on this box has no base domain")
	}
	if err := edge.RefuseUnusablePreviewBase(base); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return nil
}

func Claim(claims []boxapi.Claim, taken boxapi.Claim) ([]boxapi.Claim, error) {
	if at := slices.IndexFunc(claims, func(claim boxapi.Claim) bool { return claim.Hostname == taken.Hostname }); at >= 0 && claims[at].Owner != taken.Owner {
		return nil, refusal.Refuse(refusal.CodeBusy,
			"%s is claimed on this box by %s\nUnbind it there before %s binds it",
			taken.Hostname, claims[at].Owner, taken.Owner)
	}
	kept := slices.DeleteFunc(slices.Clone(claims), func(claim boxapi.Claim) bool { return claim.Hostname == taken.Hostname })
	return append(kept, taken), nil
}

func Disclaim(claims []boxapi.Claim, dropped func(boxapi.Claim) bool) []boxapi.Claim {
	return slices.DeleteFunc(slices.Clone(claims), dropped)
}

func Route(routes []boxapi.Route, taken boxapi.Route) []boxapi.Route {
	return append(Unroute(routes, func(route boxapi.Route) bool { return route.RouteKey == taken.RouteKey }), taken)
}

func Unroute(routes []boxapi.Route, dropped func(boxapi.Route) bool) []boxapi.Route {
	return slices.DeleteFunc(slices.Clone(routes), dropped)
}

func RenderProxyConfig(front proxy.Proxy, state boxapi.RoutingTable) ([]byte, error) {
	if err := RefuseInvalid(state); err != nil {
		return nil, err
	}
	rendered, err := front.Render(Spec(state))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return rendered, nil
}

func RenderOriginFiles(front proxy.Proxy, state boxapi.RoutingTable) ([]proxy.OriginFile, error) {
	files, err := front.OriginFiles(Spec(state))
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return files, nil
}

func Spec(state boxapi.RoutingTable) proxy.Spec {
	pins := make([]proxy.Pin, 0, len(state.Pins))
	for _, pin := range state.Pins {
		pins = append(pins, proxy.Pin{Hostname: pin.Hostname, Path: pin.Path})
	}
	shields := make([]proxy.Shield, 0, len(state.Shields))
	for _, shield := range state.Shields {
		shields = append(shields, proxy.Shield{
			Hostname: shield.Hostname, ClientCAs: shield.ClientCAs, OriginCertificate: shield.OriginCertificate,
		})
	}
	return proxy.Spec{
		Pins:        pins,
		Shields:     shields,
		Hostnames:   state.Hostnames(),
		PreviewBase: state.PreviewBase,
		Upstream:    SwitchboardUpstream,
		Router:      boxapi.SwitchboardRouter,
		Permission:  SwitchboardPermission,
	}
}

func RefuseInvalid(state boxapi.RoutingTable) error {
	if err := state.Validate(); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return nil
}

func Destination(front proxy.Proxy) string {
	if file := front.File(); file != boxapi.ProxyConfig {
		return file
	}
	return ""
}
