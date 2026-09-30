package host

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	MoveGroupKind    = "proxy"
	KindCertificates = "tls:certificates"
)

const moveWait = 10 * time.Minute

var movePauses = retryBackoff{base: 2, ceiling: 15, spread: 2}

type frontMove struct {
	host     *Host
	from     Front
	to       Front
	table    RoutingTable
	recorded Item
	resumed  bool
}

func (m *frontMove) recordUnfinished() []Item {
	if m == nil {
		return nil
	}
	return []Item{m.recorded}
}

func (f Front) identifyProcess() string {
	switch {
	case f.Manual != nil:
		return "manual"
	case f.Traefik != nil:
		return "traefik"
	case f.Caddy != nil:
		return "caddy " + f.Caddy.Container
	default:
		return ""
	}
}

func (f Front) identifyResolvers() string {
	if f.Traefik == nil {
		return ""
	}
	return f.Traefik.Resolver + " " + f.Traefik.PreviewResolver
}

func (f Front) resolvePlacedFile() string { return destination(openFront(f, frontBox{})) }

func (m frontMove) isSameProcess() bool {
	return m.from.adopted() && m.to.adopted() && m.from.identifyProcess() == m.to.identifyProcess()
}

func (m frontMove) isSwitchboardMoved() bool {
	return !slices.Equal(m.from.published(), m.to.published()) || !slices.Equal(m.from.listening(), m.to.listening())
}

func (m frontMove) isPlacedFileMoved() bool {
	at := m.from.resolvePlacedFile()
	return at != "" && at != m.to.resolvePlacedFile()
}

func (m frontMove) describeOutage() string {
	switch {
	case !m.from.adopted():
		return "outage from " + caddy.Container + " stopping until " + m.to.named() + " holds 443 and has its certificates"
	case !m.to.adopted():
		return "outage from " + m.from.named() + " stopping until " + caddy.Container + " answers"
	case !m.isSameProcess():
		return "outage from " + m.from.named() + " stopping until " + m.to.named() + " serves"
	case m.isSwitchboardMoved():
		return "outage from ocel's switchboard restarting until " + m.to.named() + " forwards " + m.to.describeForwarding()
	default:
		return "no outage: " + m.from.named() + " serves throughout"
	}
}

func (m frontMove) countReissuedHostnames() int {
	if m.isSameProcess() && m.from.identifyResolvers() == m.to.identifyResolvers() {
		return 0
	}
	return len(m.to.listCertifiedHostnames(m.table))
}

func (f Front) listCertifiedHostnames(table RoutingTable) []string {
	spec := proxySpec(table)
	var wildcard certs.Leaf
	if f.Traefik != nil && f.Traefik.PreviewResolver != "" && table.PreviewBase != "" {
		wildcard.Domains = []string{edge.PreviewWildcard(table.PreviewBase)}
	}
	var certified []string
	for _, hostname := range table.hostnames() {
		shield, shielded := spec.ShieldOf(hostname)
		switch {
		case shielded && shield.OriginCertificate.Certificate != "":
		case !f.adopted() && Covering(table.Pins, hostname) != "":
		case wildcard.Covers(hostname):
			certified = append(certified, wildcard.Domains[0])
		default:
			certified = append(certified, hostname)
		}
	}
	slices.Sort(certified)
	return slices.Compact(certified)
}

func (m frontMove) listCertificateChanges() []provider.Change {
	count := m.countReissuedHostnames()
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

func (m frontMove) listRemovals() []removal {
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
	if m.isPlacedFileMoved() {
		at := m.from.resolvePlacedFile()
		placed := taking(KindPlaced, at, "ocel's routes in "+m.from.named()+"'s directory")
		placed.origins = filepath.Dir(at)
		placed.removedByPath = m.isSameProcess() && m.from.Traefik != nil
		removed = append(removed, placed)
	}
	if len(m.from.reloadGrant()) > 0 && len(m.to.reloadGrant()) == 0 {
		removed = append(removed, taking(KindFile, sudoersCaddyReload, ""))
	}
	return removed
}

var movedNames = []string{caddy.Container, SwitchboardContainer, proxyRoot, caddy.PinsDir, ProxyData, ProxyConfig, sudoersCaddyReload, FrontRecordPath}

func (m frontMove) splitOffGroup(core []provider.Change) (provider.ChangeGroup, []provider.Change) {
	group := provider.ChangeGroup{
		Kind:   MoveGroupKind,
		Name:   m.from.named() + " → " + m.to.named(),
		Action: provider.ActionReplace,
		Reason: m.describeOutage(),
	}
	for _, taken := range m.listRemovals() {
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
	if at := m.to.resolvePlacedFile(); at != "" {
		action := provider.ActionCreate
		if at == m.from.resolvePlacedFile() {
			action = provider.ActionUpdate
		}
		group.Changes = append(group.Changes, provider.Change{Kind: KindPlaced, Name: at, Action: action, Reason: "ocel's routes in " + m.to.named() + "'s directory"})
	}
	group.Changes = append(group.Changes, m.listCertificateChanges()...)
	return group, kept
}

func (h *Host) readMovedTable(ctx context.Context) (RoutingTable, error) {
	pair, err := h.currentPair(ctx)
	if err != nil || pair.table == nil {
		return RoutingTable{}, err
	}
	return ReadRoutingTable(pair.table)
}

func (m *frontMove) removeOldFront(ctx context.Context, progress progress.Log) error {
	if m == nil || m.isSameProcess() && m.from.Caddy == nil {
		return nil
	}
	return m.host.removeAll(ctx, m.listRemovals(), progress)
}

func (h *Host) removeAll(ctx context.Context, removals []removal, progress progress.Log) error {
	for _, taken := range removals {
		removed, err := h.remove(ctx, taken)
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

func (m *frontMove) placeRoutes(ctx context.Context) error {
	if m.isSameProcess() || !m.to.adopted() {
		return m.host.rerender(ctx)
	}
	return m.host.placeUnserved(ctx)
}

func (m *frontMove) awaitFront(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if m == nil || !m.to.adopted() {
		return nil
	}
	switch {
	case !m.isSameProcess():
		say(progress, "Start your proxy on 80 and 443 now")
	case m.to.Manual != nil:
		say(progress, "Forward your proxy "+m.to.describeForwarding()+" now")
	case m.to.Traefik != nil && m.isPlacedFileMoved():
		if err := m.awaitPlacement(ctx, tier); err != nil {
			return err
		}
		if err := m.host.removeAll(ctx, m.listRemovals(), progress); err != nil {
			return err
		}
	}
	waiting, failures, err := m.host.awaitSwitchboardAnswers(ctx, m.table.hostnames(), m.host.ProbeRouter)
	if err != nil || len(waiting) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s did not serve %s through ocel's switchboard within %s:\n%s\n"+
			"When it serves them, run `%s` to finish the move",
		m.to.named(), strings.Join(waiting, ", "), moveWait, strings.Join(failures, "\n"), provider.BootstrapCommand(tier))
}

func (m *frontMove) awaitPlacement(ctx context.Context, tier environment.Tier) error {
	placed, old := m.to.resolvePlacedFile(), m.from.resolvePlacedFile()
	if _, err := m.host.run(ctx, "touch "+old+" so "+m.to.named()+" reads its directory again", "touch -c "+quoted(old), nil); err != nil {
		return err
	}
	hostname := traefik.DerivePlacementHostname(placed)
	waiting, failures, err := m.host.awaitSwitchboardAnswers(ctx, []string{hostname}, m.host.probeAnyCertificate)
	if err != nil || len(waiting) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s did not read ocel's file at %s within %s: %s, the name only that file routes, did not reach ocel's switchboard:\n%s\n"+
			"Ocel's file at %s still routes your hostnames. Make %s read %s, then run `%s` to finish the move",
		m.to.named(), placed, moveWait, hostname, strings.Join(failures, "\n"),
		old, m.to.named(), filepath.Dir(placed), provider.BootstrapCommand(tier))
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

func (h *Host) probeAnyCertificate(ctx context.Context, hostname string) (Answer, error) {
	return h.probe(ctx, "probe "+hostname+" on this box's own https port accepting any certificate", hostname, "--any-certificate")
}

func (h *Host) awaitSwitchboardAnswers(ctx context.Context, hostnames []string, probe func(context.Context, string) (Answer, error)) ([]string, []string, error) {
	failures := map[string]string{}
	waiting := hostnames
	var waited time.Duration
	for try := 1; ; try++ {
		var still []string
		for _, hostname := range waiting {
			said, err := probe(ctx, hostname)
			if err != nil {
				return nil, nil, err
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
		if waiting = still; len(waiting) == 0 || waited >= moveWait {
			break
		}
		pause := movePauses.after(try)
		if err := h.pause(ctx, pause); err != nil {
			return nil, nil, err
		}
		waited += pause
	}
	reasons := make([]string, 0, len(waiting))
	for _, hostname := range waiting {
		reasons = append(reasons, failures[hostname])
	}
	return waiting, reasons, nil
}
