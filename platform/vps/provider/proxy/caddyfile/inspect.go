package caddyfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const Renewal = "your Caddy renews it for as long as a site block names the hostname"

const collisionSubject = "hostnames your Caddy also serves"

func (c Caddyfile) inspect(ctx context.Context) (proxy.Checks, error) {
	claimed, err := c.Box.Claimed(ctx)
	if err != nil {
		return nil, err
	}
	admin := provider.HostCheck{Subject: c.named() + " admin endpoint", Verdict: provider.HostPass,
		Finding: fmt.Sprintf("%s answers on %s", c.named(), adminServers)}
	sites, unread := c.sites(ctx)
	if unread != nil {
		admin.Verdict, admin.Finding, admin.Fix = provider.HostFail, unread.Error(), "restore `admin localhost:2019` in your Caddy's global options"
	}
	checks := proxy.Checks{admin}
	if unread == nil {
		checks = append(checks, c.imported(sites, claimed), collisions(sites, claimed))
	}
	placed, err := c.placedCheck(ctx, claimed)
	if err != nil {
		return nil, err
	}
	checks = append(checks, placed)
	if c.Network != "" {
		checks = append(checks, c.memberCheck(ctx))
	}
	for _, hostname := range claimed {
		check, err := c.routing(ctx, hostname)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (c Caddyfile) imported(sites []site, claimed []string) provider.HostCheck {
	check := provider.HostCheck{Subject: "import of " + c.File(), Verdict: provider.HostPass,
		Finding: fmt.Sprintf("%s serves every hostname %s names", c.named(), FileName)}
	var served []string
	for _, each := range sites {
		if each.ocels {
			served = append(served, each.hosts...)
		}
	}
	var missing []string
	for _, hostname := range claimed {
		if !slices.ContainsFunc(served, func(host string) bool { return strings.EqualFold(host, hostname) }) {
			missing = append(missing, hostname)
		}
	}
	if len(missing) > 0 {
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("the config %s runs holds no route of ocel's for %s, so the import of %s is not in effect",
			c.named(), strings.Join(missing, ", "), c.File())
		check.Fix = fmt.Sprintf("add `import %s` to your Caddyfile and reload your Caddy", filepath.Join(c.Directory, "*.caddy"))
	}
	return check
}

func collisions(sites []site, claimed []string) provider.HostCheck {
	check := provider.HostCheck{Subject: collisionSubject, Verdict: provider.HostPass,
		Finding: "no site of yours serves a hostname ocel serves"}
	var found []string
	for _, hostname := range claimed {
		if theirs, host, taken := collision(sites, hostname); taken {
			found = append(found, fmt.Sprintf("%s by %s, matching host %s", hostname, theirs, host))
		}
	}
	if len(found) > 0 {
		check.Verdict = provider.HostFail
		check.Finding = "your Caddy also serves " + strings.Join(found, "; ") + ", and whichever file it reads first wins"
		check.Fix = "remove those hostnames from your own sites, or unbind them from ocel"
	}
	return check
}

func sum(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (c Caddyfile) placedCheck(ctx context.Context, claimed []string) (provider.HostCheck, error) {
	check := provider.HostCheck{Subject: c.File(), Verdict: provider.HostPass, Finding: FileName + " is what ocel renders"}
	placed, err := c.Box.Placed(ctx, c.File())
	if err != nil {
		return check, err
	}
	if placed != sum(c.render(claimed)) {
		check.Verdict = provider.HostFail
		check.Finding = fmt.Sprintf("%s is missing or not what ocel renders from this box's routes", c.File())
		check.Fix = "run a deploy, which places it again"
	}
	return check, nil
}

func (c Caddyfile) memberCheck(ctx context.Context) provider.HostCheck {
	check := provider.HostCheck{Subject: switchboard.Name + " on " + c.Network, Verdict: provider.HostFail,
		Fix: "run `ocel bootstrap production`, which starts the switchboard on " + c.Network}
	said, err := c.Box.Ran(ctx, "read the networks "+switchboard.Name+" is on", []string{"docker", "inspect", "--type", "container",
		"--format", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}", switchboard.Name})
	switch {
	case err != nil:
		check.Finding = err.Error()
	case !slices.Contains(strings.Fields(said), c.Network):
		check.Finding = fmt.Sprintf("%s is on %s and not on %s, where your Caddy reaches it by name", switchboard.Name, strings.TrimSpace(said), c.Network)
	default:
		check.Verdict, check.Fix = provider.HostPass, ""
		check.Finding = fmt.Sprintf("%s is on %s, where your Caddy reaches it", switchboard.Name, c.Network)
	}
	return check
}

func (c Caddyfile) routing(ctx context.Context, hostname string) (provider.HostCheck, error) {
	check := provider.HostCheck{Subject: hostname, Verdict: provider.HostFail,
		Fix: fmt.Sprintf("check %s imports %s and was reloaded", c.named(), c.File())}
	answered, failure, err := c.Box.Probe(ctx, hostname)
	switch {
	case err != nil:
		return check, err
	case failure != "":
		check.Finding = failure
	case answered != switchboard.RouterKind:
		check.Finding = fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, answered)
	default:
		check.Verdict, check.Fix = provider.HostPass, ""
		check.Finding = fmt.Sprintf("your Caddy routes %s to ocel's switchboard", hostname)
	}
	return check, nil
}
