package host

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	MoveGroupKind    = "proxy"
	KindCertificates = "tls:certificates"
)

const moveWait = 10 * time.Minute

var movePauses = retryBackoff{base: 2, ceiling: 15, spread: 2}

type frontMove struct {
	from      Front
	to        Front
	hostnames []string
}

func (m frontMove) sameProcess() bool {
	switch {
	case m.from.Manual != nil && m.to.Manual != nil, m.from.Traefik != nil && m.to.Traefik != nil:
		return true
	case m.from.Caddy != nil && m.to.Caddy != nil:
		return m.from.Caddy.Container == m.to.Caddy.Container
	default:
		return false
	}
}

func (m frontMove) outage() string {
	switch {
	case !m.from.adopted():
		return "outage from " + caddy.Container + " stopping until " + m.to.named() + " holds 443 and has its certificates"
	case !m.to.adopted():
		return "outage from " + m.from.named() + " stopping until " + caddy.Container + " answers"
	case m.sameProcess():
		return "no outage: " + m.from.named() + " serves throughout"
	default:
		return "outage from " + m.from.named() + " stopping until " + m.to.named() + " serves"
	}
}

func (m frontMove) reissued() int {
	if !m.sameProcess() {
		return len(m.hostnames)
	}
	if m.from.Traefik != nil && (m.from.Traefik.Resolver != m.to.Traefik.Resolver || m.from.Traefik.PreviewResolver != m.to.Traefik.PreviewResolver) {
		return len(m.hostnames)
	}
	return 0
}

func (m frontMove) certificates() []provider.Change {
	count := m.reissued()
	if count == 0 {
		return nil
	}
	named := fmt.Sprintf("%d hostnames get new certificates from %s", count, m.to.named())
	if count == 1 {
		named = "1 hostname gets a new certificate from " + m.to.named()
	}
	return []provider.Change{{
		Kind:   KindCertificates,
		Name:   named,
		Action: provider.ActionCreate,
		Reason: "each first-time hostname counts toward Let's Encrypt's 50 new certificates per registered domain per week",
	}}
}

func (m frontMove) oldPlaced() string { return destination(openFront(m.from, frontBox{})) }

func (m frontMove) newPlaced() string { return destination(openFront(m.to, frontBox{})) }

func (m frontMove) removals(unmounted bool) []removal {
	if !m.from.adopted() {
		return []removal{
			taking(KindContainer, caddy.Container, "ocel's front proxy"),
			taking(KindProxyConfig, ProxyConfig, ""),
			taking(KindDir, ProxyData, "certificates and acme key"),
			taking(KindDir, proxyRoot, ""),
			sharing(caddy.PinsDir, "only if empty: it holds your pinned certificates"),
		}
	}
	var removed []removal
	if at := m.oldPlaced(); at != "" && at != m.newPlaced() {
		placed := taking(KindPlaced, at, "ocel's routes in "+m.from.named()+"'s directory")
		placed.unmounted = unmounted
		if m.from.Caddy != nil {
			placed.origins = filepath.Dir(at)
		}
		if m.from.Caddy != nil && m.sameProcess() {
			placed.reload = m.from.caddyReloadCommand()
		}
		removed = append(removed, placed)
	}
	if len(m.from.reloadGrant()) > 0 && len(m.to.reloadGrant()) == 0 {
		removed = append(removed, taking(KindFile, sudoersCaddyReload, ""))
	}
	return removed
}

var movedNames = []string{caddy.Container, SwitchboardContainer, proxyRoot, caddy.PinsDir, ProxyData, ProxyConfig, sudoersCaddyReload, FrontRecordPath}

func (m frontMove) group(core []provider.Change) (provider.ChangeGroup, []provider.Change) {
	group := provider.ChangeGroup{
		Kind:   MoveGroupKind,
		Name:   m.from.named() + " → " + m.to.named(),
		Action: provider.ActionReplace,
		Reason: m.outage(),
	}
	for _, taken := range m.removals(false) {
		group.Changes = append(group.Changes, provider.Change{Kind: taken.kind, Name: taken.path, Action: taken.action, Reason: taken.reason})
	}
	kept := make([]provider.Change, 0, len(core))
	for _, change := range core {
		if change.Action.Writes() && slices.Contains(movedNames, change.Name) {
			group.Changes = append(group.Changes, change)
			continue
		}
		kept = append(kept, change)
	}
	if at := m.newPlaced(); at != "" {
		action := provider.ActionCreate
		if at == m.oldPlaced() {
			action = provider.ActionUpdate
		}
		group.Changes = append(group.Changes, provider.Change{Kind: KindPlaced, Name: at, Action: action, Reason: "ocel's routes in " + m.to.named() + "'s directory"})
	}
	group.Changes = append(group.Changes, m.certificates()...)
	return group, kept
}

func (h *Host) claimedHostnames(ctx context.Context) ([]string, error) {
	pair, err := h.currentPair(ctx)
	if err != nil || pair.table == nil {
		return nil, err
	}
	table, err := ReadRoutingTable(pair.table)
	if err != nil {
		return nil, err
	}
	return table.hostnames(), nil
}

func (b Bootstrap) removeOldFront(ctx context.Context, move *frontMove, progress progress.Log) error {
	if move == nil || move.sameProcess() {
		return nil
	}
	return b.removeAll(ctx, move.removals(false), progress)
}

func (b Bootstrap) removeAll(ctx context.Context, removals []removal, progress progress.Log) error {
	for _, taken := range removals {
		removed, err := b.host.remove(ctx, taken)
		if err != nil {
			return err
		}
		if !removed {
			say(progress, taken.kept())
			continue
		}
		say(progress, "Removed "+taken.phrase())
	}
	return nil
}

func (b Bootstrap) placeRoutes(ctx context.Context, move *frontMove) error {
	if move == nil || move.sameProcess() || !move.to.adopted() {
		return b.host.rerender(ctx)
	}
	return b.host.placeUnserved(ctx)
}

func (b Bootstrap) awaitFront(ctx context.Context, tier environment.Tier, move *frontMove, progress progress.Log) error {
	if move == nil || !move.to.adopted() {
		return nil
	}
	if !move.sameProcess() {
		say(progress, "Start your proxy on 80 and 443 now")
	}
	if err := b.host.awaitServed(ctx, move.to.named(), move.hostnames, tier); err != nil {
		return err
	}
	if !move.sameProcess() {
		return nil
	}
	return b.removeAll(ctx, move.removals(true), progress)
}

func (h *Host) placeUnserved(ctx context.Context) error {
	at := destination(h.front)
	if at == "" {
		return nil
	}
	pair, err := h.currentTable(ctx)
	if err != nil {
		return err
	}
	table, err := ReadRoutingTable(pair.table)
	if err != nil {
		return err
	}
	rendered, err := RenderProxyConfig(h.front, table)
	if err != nil {
		return err
	}
	origins, err := RenderOriginFiles(h.front, table)
	if err != nil {
		return err
	}
	return h.replace(ctx, pair.digest(), at, rendered, origins)
}

func (h *Host) awaitServed(ctx context.Context, named string, hostnames []string, tier environment.Tier) error {
	failures := map[string]string{}
	waiting := hostnames
	var waited time.Duration
	for try := 1; ; try++ {
		var still []string
		for _, hostname := range waiting {
			said, err := h.ProbeRouter(ctx, hostname)
			if err != nil {
				return err
			}
			switch {
			case said.Router == switchboard.RouterKind:
				continue
			case said.Failure != "":
				failures[hostname] = said.Failure
			default:
				failures[hostname] = fmt.Sprintf("%s answers on this box's 443 as %q, not through ocel's switchboard", hostname, said.Router)
			}
			still = append(still, hostname)
		}
		if waiting = still; len(waiting) == 0 {
			return nil
		}
		if waited >= moveWait {
			break
		}
		pause := movePauses.after(try)
		if err := h.pause(ctx, pause); err != nil {
			return err
		}
		waited += pause
	}
	reasons := make([]string, 0, len(waiting))
	for _, hostname := range waiting {
		reasons = append(reasons, failures[hostname])
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s did not serve %s through ocel's switchboard within %s:\n%s\n"+
			"When it serves them, run `%s` to finish the move",
		named, strings.Join(waiting, ", "), moveWait, strings.Join(reasons, "\n"), provider.BootstrapCommand(tier))
}
