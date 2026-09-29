package caddyfile

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

var _ proxy.Proxy = Caddyfile{}

func (Caddyfile) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (c Caddyfile) Render(spec proxy.Spec) ([]byte, error) { return c.render(spec) }

func (c Caddyfile) File() string { return filepath.Join(c.Directory, FileName) }

func (Caddyfile) Unrendered([]byte, proxy.Permission) string { return "" }

func (c Caddyfile) RefuseRouted(ctx context.Context, hostnames []string) error {
	placed, err := c.Box.ReadSpec(ctx)
	if err != nil {
		return err
	}
	sites, err := c.sites(ctx)
	if err != nil {
		return err
	}
	for _, hostname := range hostnames {
		if theirs, host, taken := collision(sites, placed, hostname); taken {
			return refusal.Refuse(refusal.CodeBusy,
				"%s is already served by %s: %s matches host %s\n"+
					"Ocel never takes a hostname your Caddy serves; remove it from that site and reload your Caddy, or bind another hostname",
				hostname, c.named(), theirs, host)
		}
	}
	return nil
}

func (c Caddyfile) Validate(ctx context.Context, rendered []byte) error {
	if _, err := c.Box.RanWithStdin(ctx, "adapt "+FileName+" with "+c.named(), c.adapting(), rendered); err != nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s cannot adapt the %s ocel rendered, so it was not placed: %v", c.named(), FileName, err)
	}
	return nil
}

func (c Caddyfile) Reload(ctx context.Context, _ proxy.Spec) error {
	if _, err := c.Box.Ran(ctx, "reload "+c.named(), c.Reloading()); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s refused the reload and keeps serving the config it had: %v\nThe error can be in your own Caddyfile as well as in %s",
			c.named(), err, FileName)
	}
	return nil
}

func (c Caddyfile) Inspect(ctx context.Context) (proxy.Checks, error) {
	placed, err := c.Box.ReadSpec(ctx)
	if err != nil {
		return nil, err
	}
	sites, unread := c.sites(ctx)
	checks := proxy.Checks{c.adminCheck(unread)}
	if unread == nil {
		checks = append(checks, c.imported(sites, placed), collisions(sites, placed))
	}
	current, err := c.placedCheck(ctx, placed)
	if err != nil {
		return nil, err
	}
	checks = append(checks, current)
	if c.Network != "" {
		checks = append(checks, c.memberCheck(ctx))
	}
	_, shielded := split(placed)
	for _, hostname := range placed.Hostnames {
		probe := c.routing
		if slices.ContainsFunc(shielded, func(site shieldedSite) bool { return site.hostname == hostname }) {
			probe = c.shieldCheck
		}
		check, err := probe(ctx, hostname)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (Caddyfile) RefuseUnshielded(context.Context, string) error { return nil }

func (c Caddyfile) OriginFiles(spec proxy.Spec) ([]proxy.OriginFile, error) {
	return c.originFiles(spec), nil
}

func (Caddyfile) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{Renewal: Renewal}, nil
}
