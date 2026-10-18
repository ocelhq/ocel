package host

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Host struct {
	*ssh.Box
	deploy      Keys
	pins        []Pin
	proxyOption Front
	front       proxy.Proxy

	keying sync.Mutex
	keyed  []byte

	pinning  sync.Mutex
	vouched  bool
	unusable error

	described sync.Mutex
	readings  map[environment.Tier]Reading

	engine *Engine
}

func New(box *ssh.Box, deploy Keys, pins []Pin, front Front) *Host {
	h := &Host{Box: box, engine: NewEngine(box), deploy: deploy, pins: pins, proxyOption: front}
	h.front = openFront(front, frontBox{h})
	return h
}

func (h *Host) Pins() []Pin { return slices.Clone(h.pins) }

func (h *Host) PinFor(hostname string) string { return Covering(h.pins, hostname) }

func (h *Host) Install(ctx context.Context, item Item) error {
	_, err := h.Run(ctx, ssh.Root, "write "+item.ID(), item.command(), item.stdin())
	return err
}

func (h *Host) remove(ctx context.Context, taken removal) (bool, error) {
	switch taken.kind {
	case KindUser:
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.kind+" "+taken.path, "userdel -f "+shell.Quote(taken.path), nil)
		return err == nil, err
	case KindNetwork:
		rendered, err := h.Run(ctx, ssh.Root, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return strings.TrimSpace(rendered) != networkInUse, err
	case KindContainer, KindApps, KindResourceVolumes, KindAppNetworks:
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return err == nil, err
	case KindUnit:
		if !slices.Contains([]string{LiveService, LiveSocketUnit, BackupsTimer,
			EnvSourceSyncService(environment.TierProduction), EnvSourceSyncService(environment.TierPreview)}, taken.path) {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%s %s is not ocel's to remove", taken.kind, taken.path)
		}
		_, err := h.Run(ctx, ssh.Root, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return err == nil, err
	case KindDir, KindFile, KindSealKey, KindProxyConfig, KindRoutingTable, KindPlaced:
		if !strings.HasPrefix(taken.path, "/") {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%q is not an absolute path", taken.path)
		}
		rendered, err := h.Run(ctx, ssh.Root, "remove "+taken.path, taken.command(), nil)
		if taken.kind == KindDir && taken.shared {
			return strings.TrimSpace(rendered) != dirNonEmpty, err
		}
		return err == nil, err
	default:
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s %s is not ocel's to remove", taken.kind, taken.path)
	}
}

type Reading struct {
	Tier     environment.Tier
	Present  bool
	Keys     []byte
	Arch     string
	Stamp    Stamp
	Seal     Seal
	Observed map[string]string
	Front    Front
	Engine   InstalledEngine

	recorded    []Item
	move        *frontMove
	unelevated  bool
	rerendering bool
}

func (r Reading) current(item Item) bool { return r.Observed[item.ID()] == item.Digest() }

func (r Reading) observed(kind, path string) bool {
	_, seen := r.Observed[kind+" "+path]
	return seen
}

func (r Reading) unfinished() bool { return r.Present && r.Stamp.State != StateComplete }

func (r Reading) Items() []Item { return partItems(new(Host).parts(), r) }

func (r Reading) upToDate() bool {
	items := r.Items()
	if !r.Present || r.Stamp.State != StateComplete || !r.Stamp.records(items) {
		return false
	}
	if r.unelevated {
		return true
	}
	if r.Seal.Fingerprint == "" || r.Seal.Fingerprint != r.Stamp.Seal.Fingerprint {
		return false
	}
	for _, item := range items {
		if !r.current(item) {
			return false
		}
	}
	return true
}

func (h *Host) rememberReading(read Reading) {
	h.described.Lock()
	defer h.described.Unlock()
	if h.readings == nil {
		h.readings = map[environment.Tier]Reading{}
	}
	h.readings[read.Tier] = read
}

func (h *Host) recallReading(tier environment.Tier) (Reading, bool) {
	h.described.Lock()
	defer h.described.Unlock()
	read, remembered := h.readings[tier]
	return read, remembered
}

func (h *Host) forgetReading(tier environment.Tier) {
	h.described.Lock()
	defer h.described.Unlock()
	delete(h.readings, tier)
}

func (h *Host) Read(ctx context.Context, tier environment.Tier) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.read(ctx, tier, keys, ssh.Root)
}

func (h *Host) Observe(ctx context.Context, tier environment.Tier) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	denied := h.Elevation(ctx)
	read, err := h.read(ctx, tier, keys, ssh.RootWhenAble)
	if err != nil {
		return Reading{}, err
	}
	read.unelevated = denied != nil
	return read, nil
}

func (h *Host) Survey(ctx context.Context, tier environment.Tier) (Reading, error) {
	return h.observing(ctx, tier, nil, ssh.Root)
}

func (h *Host) observing(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		arch = ssh.AMD64
	}
	return h.surveyed(ctx, tier, keys, arch, as)
}

func (h *Host) observe(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	arch, err := h.Arch(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.surveyed(ctx, tier, keys, arch, as)
}

func (h *Host) surveyed(ctx context.Context, tier environment.Tier, keys []byte, arch string, as ssh.Acting) (Reading, error) {
	surveying := Items(tier, keys, arch, h.proxyOption)
	if h.proxyOption.adopted() {
		surveying = append(surveying, frontProxy().item(""))
	}
	rendered, err := h.Run(ctx, as, "survey what "+string(tier)+" has installed", survey(surveying, StampPath(tier), FrontRecordPath, imagesLock), nil)
	if err != nil {
		return Reading{}, err
	}
	observed, seal, err := readSurvey(rendered)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Tier: tier, Keys: keys, Arch: arch, Seal: seal, Observed: observed, Front: h.proxyOption, Engine: readEngine(rendered)}, nil
}

func (h *Host) read(ctx context.Context, tier environment.Tier, keys []byte, as ssh.Acting) (Reading, error) {
	read, err := h.observe(ctx, tier, keys, as)
	if err != nil {
		return Reading{}, err
	}
	if _, stamped := read.Observed[KindFile+" "+StampPath(tier)]; !stamped {
		return read, nil
	}
	stamp, err := h.readStamp(ctx, tier, as)
	if err != nil {
		return Reading{}, err
	}
	read.Present, read.Stamp = true, stamp
	return read, nil
}

func (h *Host) readStamp(ctx context.Context, tier environment.Tier, as ssh.Acting) (Stamp, error) {
	rendered, err := h.Run(ctx, as, "read the stamp", "cat "+shell.Quote(StampPath(tier)), nil)
	if err != nil {
		return Stamp{}, err
	}
	var stamp Stamp
	if err := json.Unmarshal([]byte(rendered), &stamp); err != nil {
		return Stamp{}, refusal.Refuse(refusal.CodeInvalid,
			"%s is not a stamp this ocel can read: %s", StampPath(tier), err)
	}
	return stamp, nil
}

func (h *Host) Stamp(ctx context.Context, tier environment.Tier, stamp Stamp) error {
	item, err := stamp.item(tier)
	if err != nil {
		return err
	}
	return h.Install(ctx, item)
}

func survey(items []Item, also ...string) string {
	return surveying(`[ -f "$p" ]`, items, also...)
}

func surveying(file string, items []Item, also ...string) string {
	var probes, script strings.Builder
	script.WriteString("for p in")
	for _, item := range items {
		if probe := item.probe(); probe != "" {
			probes.WriteString(probe + "\n")
			continue
		}
		script.WriteString(" " + shell.Quote(item.Name))
	}
	for _, path := range also {
		script.WriteString(" " + shell.Quote(path))
	}
	stated, owner := `"$(stat -c %a "$p")"`, `"$(stat -c %U "$p")"`
	script.WriteString(`; do
if [ -h "$p" ]; then ` + reports(shell.Quote(kindLink), `"$p"`, `0`, `''`, `"$(readlink "$p")"`) + `
elif [ -d "$p" ]; then ` + reports(shell.Quote(KindDir), `"$p"`, stated, owner, `''`) + `
elif ` + file + `; then ` + reports(shell.Quote(KindFile), `"$p"`, stated, owner, `"$(sha256sum "$p" | cut -d' ' -f1)"`) + `
fi
done`)
	return probes.String() + script.String()
}

const (
	kindLink       = "fs:link"
	kindUnreadable = "probe:unreadable"
)

func unreadable(kind, name, why string) string {
	return reports(shell.Quote(kindUnreadable), name, "0", shell.Quote(kind), why)
}

func column(columns []string, at int, absent string) string {
	if at < len(columns) && columns[at] != "" {
		return columns[at]
	}
	return absent
}

func remainder(columns []string, separator, absent string) string {
	if len(columns) > 4 && columns[4] != "" {
		return strings.Join(columns[4:], separator)
	}
	return absent
}

const unnamedPath = "(unnamed)"

func couldNotLook(columns []string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"could not check %s %s on this host: %s",
		column(columns, 3, "path"),
		column(columns, 1, unnamedPath),
		remainder(columns, " ", "no reason given"))
}

func pointedAway(columns []string) error {
	named := column(columns, 1, unnamedPath)
	return refusal.Refuse(refusal.CodeDenied,
		"%s is a symbolic link to %s\n"+
			"Put a real directory or file at %s",
		named, remainder(columns, "\t", "an unreadable target"), named)
}

func reports(kind, name, mode, owner, sum string) string {
	return strings.Join([]string{`printf '%s\t%s\t%s\t%s\t%s\n'`, kind, name, mode, owner, sum}, " ")
}

func readSurvey(rendered string) (map[string]string, Seal, error) {
	observed := make(map[string]string)
	var seal Seal
	for _, line := range strings.Split(strings.Trim(rendered, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if columns[0] == engineFactsRow {
			continue
		}
		if columns[0] == kindLink {
			return nil, Seal{}, pointedAway(columns)
		}
		if columns[0] == kindUnreadable {
			return nil, Seal{}, couldNotLook(columns)
		}
		sealed := columns[0] == KindSealKey
		if (sealed && len(columns) != 6) || (!sealed && len(columns) != 5) {
			return nil, Seal{}, refusal.Refuse(refusal.CodeDenied,
				"the host answered a survey line ocel cannot read: %q", line)
		}
		parsed, err := mode(columns[2])
		if err != nil {
			return nil, Seal{}, refusal.Refuse(refusal.CodeDenied,
				"the host reported %q as the mode of %s", columns[2], columns[1])
		}
		content := columns[4]
		if sealed {
			seal = Seal{Fingerprint: columns[4], Algorithm: SealAlgorithm, CreatedAt: columns[5]}
			content = ""
		}
		observed[columns[0]+" "+columns[1]] = digest(columns[0], columns[1], parsed, columns[3], content)
	}
	return observed, seal, nil
}
