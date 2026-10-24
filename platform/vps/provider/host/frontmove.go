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
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const moveWait = 10 * time.Minute

var movePauses = shell.Backoff{Base: 2, Ceiling: 15, Spread: 2}

type frontMove struct {
	host     *Host
	from     front.Proxy
	to       front.Proxy
	table    boxapi.RoutingTable
	recorded machine.Item
	resumed  bool
	stranded []machine.Removal
}

func (m *frontMove) recordUnfinished() []machine.Item {
	if m == nil {
		return nil
	}
	return []machine.Item{m.recorded}
}

func resolvePlacedFile(f front.Proxy) string { return routing.Destination(f.Front().Open(frontBox{})) }

func (m frontMove) isSameProcess() bool {
	return m.from.Adopted() && m.to.Adopted() && m.from.Front().Process() == m.to.Front().Process()
}

func (m frontMove) isSwitchboardMoved() bool {
	return !slices.Equal(m.from.Front().Published(), m.to.Front().Published()) || !slices.Equal(m.from.Front().Listening(), m.to.Front().Listening())
}

func (m frontMove) isPlacedFileMoved() bool {
	at := resolvePlacedFile(m.from)
	return at != "" && at != resolvePlacedFile(m.to)
}

func (m frontMove) describeOutage() string {
	switch {
	case !m.from.Adopted():
		return "outage from " + caddy.Container + " stopping until " + m.to.Front().Named() + " holds 443 and has its certificates"
	case !m.to.Adopted():
		return "outage from " + m.from.Front().Named() + " stopping until " + caddy.Container + " answers"
	case !m.isSameProcess():
		return "outage from " + m.from.Front().Named() + " stopping until " + m.to.Front().Named() + " serves"
	case m.isSwitchboardMoved():
		return "outage from ocel's switchboard restarting until " + m.to.Front().Named() + " forwards " + front.DescribeForwarding(m.to.Front())
	default:
		return "no outage: " + m.from.Front().Named() + " serves throughout"
	}
}

func (m frontMove) countReissuedHostnames() int {
	if m.isSameProcess() && m.from.Front().Resolvers() == m.to.Front().Resolvers() {
		return 0
	}
	return len(listCertifiedHostnames(m.to, m.table))
}

func listCertifiedHostnames(f front.Proxy, table boxapi.RoutingTable) []string {
	spec := routing.Spec(table)
	var wildcard certs.Leaf
	if f.Front().SharesPreviewWildcard() && table.PreviewBase != "" {
		wildcard.Domains = []string{edge.PreviewWildcard(table.PreviewBase)}
	}
	var certified []string
	for _, hostname := range table.Hostnames() {
		shield, shielded := spec.ShieldOf(hostname)
		switch {
		case shielded && shield.OriginCertificate.Certificate != "":
		case !f.Adopted() && certs.Covering(table.Pins, hostname) != "":
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
	named := fmt.Sprintf("%d hostnames get new certificates from %s", count, m.to.Front().Named())
	if count == 1 {
		named = "1 hostname gets a new certificate from " + m.to.Front().Named()
	}
	return []provider.Change{{
		Kind:   machine.KindCertificates,
		Name:   named,
		Action: provider.ActionCreate,
		Reason: "each first-time hostname counts toward Let's Encrypt's 50 new certificates per registered domain per week",
	}}
}

func (m frontMove) listRemovals() []machine.Removal {
	if !m.from.Adopted() {
		return []machine.Removal{
			machine.Taking(machine.KindContainer, caddy.Container, "ocel's front proxy"),
			machine.Taking(machine.KindProxyConfig, ProxyConfig, ""),
			machine.Taking(machine.KindDir, ProxyData, "certificates and acme key"),
			machine.Taking(machine.KindDir, proxyRoot, ""),
			machine.Taking(machine.KindFile, sudoersPins, ""),
			machine.Sharing(boxapi.PinsDir, "only if empty: it holds your pinned certificates"),
		}
	}
	var removed []machine.Removal
	if m.isPlacedFileMoved() {
		at := resolvePlacedFile(m.from)
		placed := machine.Taking(machine.KindPlaced, at, "ocel's routes in "+m.from.Front().Named()+"'s directory")
		placed.Origins = filepath.Dir(at)
		removed = append(removed, placed)
	}
	if len(m.from.Front().ReloadGrant()) > 0 && len(m.to.Front().ReloadGrant()) == 0 {
		removed = append(removed, machine.Taking(machine.KindFile, machine.SudoersCaddyReload, ""))
	}
	return removed
}

var movedNames = []string{caddy.Container, boxapi.Switchboard, proxyRoot, boxapi.PinsDir, sudoersPins, ProxyData, ProxyConfig, machine.SudoersCaddyReload, FrontRecordPath}

func (m frontMove) splitOffGroup(core []provider.Change) (provider.ChangeGroup, []provider.Change) {
	group := provider.ChangeGroup{
		Kind:   machine.MoveGroupKind,
		Name:   m.from.Front().Named() + " → " + m.to.Front().Named(),
		Action: provider.ActionReplace,
		Reason: m.describeOutage(),
	}
	for _, taken := range append(m.listRemovals(), m.stranded...) {
		group.Changes = append(group.Changes, provider.Change{Kind: taken.Kind, Name: taken.Path, Action: taken.Action, Reason: taken.Reason})
	}
	kept := make([]provider.Change, 0, len(core))
	for _, change := range core {
		if change.Action.Writes() && slices.Contains(movedNames, change.Name) {
			group.Changes = append(group.Changes, change)
			continue
		}
		kept = append(kept, change)
	}
	if at := resolvePlacedFile(m.to); at != "" {
		action := provider.ActionCreate
		if at == resolvePlacedFile(m.from) {
			action = provider.ActionUpdate
		}
		group.Changes = append(group.Changes, provider.Change{Kind: machine.KindPlaced, Name: at, Action: action, Reason: "ocel's routes in " + m.to.Front().Named() + "'s directory"})
	}
	group.Changes = append(group.Changes, m.listCertificateChanges()...)
	return group, kept
}

func (m *frontMove) removeOldFront(ctx context.Context, progress progress.Log) error {
	if m == nil {
		return nil
	}
	if err := m.host.removeAll(ctx, m.stranded, progress); err != nil {
		return err
	}
	if m.isSameProcess() && m.from.Front().ReloadCommand() == "" {
		return nil
	}
	return m.host.removeAll(ctx, m.listRemovals(), progress)
}

func (h *Host) removeAll(ctx context.Context, removals []machine.Removal, progress progress.Log) error {
	for _, taken := range removals {
		removed, err := h.remove(ctx, taken)
		if err != nil {
			return err
		}
		if !removed {
			progress.Say(taken.Kept())
			continue
		}
		progress.Say("Removed " + taken.Phrase())
	}
	return nil
}

func (m *frontMove) placeRoutes(ctx context.Context) error {
	if m.isSameProcess() || !m.to.Adopted() {
		return m.host.routing.Rerender(ctx)
	}
	return m.host.routing.PlaceUnserved(ctx)
}

func (m *frontMove) awaitFront(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if m == nil || !m.to.Adopted() {
		return nil
	}
	switch {
	case !m.isSameProcess():
		progress.Say("Start your proxy on 80 and 443 now")
	case m.to.Front().ForwardedByHand():
		progress.Say("Forward your proxy " + front.DescribeForwarding(m.to.Front()) + " now")
	case m.to.Front().WatchesDirectory() && m.isPlacedFileMoved():
		if err := m.awaitPlacement(ctx, tier); err != nil {
			return err
		}
		if err := m.host.removeAll(ctx, m.listRemovals(), progress); err != nil {
			return err
		}
	}
	waiting, failures, err := m.host.awaitSwitchboardAnswers(ctx, m.table.Hostnames(), m.host.ProbeRouter)
	if err != nil || len(waiting) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s did not serve %s through ocel's switchboard within %s:\n%s\n"+
			"When it serves them, run `%s` to finish the move",
		m.to.Front().Named(), strings.Join(waiting, ", "), moveWait, strings.Join(failures, "\n"), provider.BootstrapCommand(tier))
}

func (m *frontMove) awaitPlacement(ctx context.Context, tier environment.Tier) error {
	placed, old := resolvePlacedFile(m.to), resolvePlacedFile(m.from)
	if _, err := m.host.Run(ctx, ssh.Root, "touch "+old+" so "+m.to.Front().Named()+" reads its directory again", "touch -c "+shell.Quote(old), nil); err != nil {
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
		m.to.Front().Named(), placed, moveWait, hostname, strings.Join(failures, "\n"),
		old, m.to.Front().Named(), filepath.Dir(placed), provider.BootstrapCommand(tier))
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
			case said.Router == boxapi.SwitchboardRouter:
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
		pause := movePauses.After(try)
		if err := h.Wait(ctx, pause); err != nil {
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
