package host

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type Conn interface {
	Stream(ctx context.Context, command string, stdin io.Reader) (session.Result, error)
	Run(ctx context.Context, command string) (string, error)
	Preflight(ctx context.Context) (session.Facts, error)
	Destination() session.Destination
}

type Dial func(ctx context.Context) (Conn, error)

type Host struct {
	dial        Dial
	deploy      Keys
	pins        []Pin
	proxyOption Front
	front       proxy.Proxy

	elevating sync.Mutex
	settled   bool
	floored   bool
	refusal   error
	elevation string

	knowing  sync.Mutex
	reported string

	rooting sync.Mutex
	knows   bool
	prefix  string

	engining sync.Mutex
	engined  bool
	engine   string

	holding sync.Mutex
	held    []byte

	pinning  sync.Mutex
	vouched  bool
	unusable error

	mu        sync.Mutex
	principal string
	tiers     map[edge.Class]bool

	pause func(context.Context, time.Duration) error
}

func New(dial Dial, deploy Keys, pins []Pin, front Front) *Host {
	h := &Host{dial: dial, deploy: deploy, pins: pins, proxyOption: front, tiers: map[edge.Class]bool{}, pause: waited}
	h.front = openFront(front, frontBox{h})
	return h
}

func waited(ctx context.Context, held time.Duration) error {
	timer := time.NewTimer(held)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (h *Host) Pins() []Pin { return slices.Clone(h.pins) }

func (h *Host) PinFor(hostname string) string { return Covering(h.pins, hostname) }

func (h *Host) holds(ctx context.Context, class edge.Class) (bool, error) {
	h.mu.Lock()
	stood := h.tiers[class]
	h.mu.Unlock()
	if stood {
		return true, nil
	}
	rendered, err := h.reach(ctx, "ask where "+string(class)+" keeps its records",
		"if [ -x "+quoted(recordsHelper)+" ] && [ -d "+quoted(RecordsDir(class))+" ]; then echo held; fi", nil)
	if err != nil || strings.TrimSpace(rendered) != "held" {
		return false, err
	}
	h.mu.Lock()
	h.tiers[class] = true
	h.mu.Unlock()
	return true, nil
}

func (h *Host) forgetTiers() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.tiers)
}

func (h *Host) remember(principal string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.principal = principal
}

func (h *Host) named() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.principal == "" {
		return "this host"
	}
	return h.principal
}

func (h *Host) Principal(ctx context.Context) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	principal := live.Destination().Principal()
	h.remember(principal)
	return principal, nil
}

func (h *Host) Address(ctx context.Context) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	address := live.Destination().Address
	if address == "" {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"%s resolves to no address", h.named())
	}
	return address, nil
}

func (h *Host) forgetting(ctx context.Context) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	return live.Destination().Forget(), nil
}

func (h *Host) elevate(ctx context.Context) (string, error) {
	h.elevating.Lock()
	defer h.elevating.Unlock()
	if h.settled {
		return h.elevation, nil
	}
	if h.floored {
		return "", h.refusal
	}
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	facts, err := live.Preflight(ctx)
	if err != nil {
		h.floored, h.refusal = true, err
		return "", err
	}
	if !facts.Root {
		h.elevation = "sudo -n "
	}
	h.settled = true
	return h.elevation, nil
}

func (h *Host) Arch(ctx context.Context) (string, error) { return h.arch(ctx) }

func (h *Host) arch(ctx context.Context) (string, error) {
	h.knowing.Lock()
	defer h.knowing.Unlock()
	if h.reported == "" {
		rendered, err := h.ran(ctx, "ask this host what it runs on", "uname -m", nil, "")
		if err != nil {
			return "", err
		}
		h.reported = strings.TrimSpace(rendered)
	}
	return Architecture(h.reported)
}

func (h *Host) reaching(ctx context.Context) string {
	elevation, err := h.elevate(ctx)
	if err != nil {
		return ""
	}
	return elevation
}

func (h *Host) rootOrSudo(ctx context.Context, live Conn) (string, error) {
	h.rooting.Lock()
	defer h.rooting.Unlock()
	if h.knows {
		return h.prefix, nil
	}
	rendered, err := live.Run(ctx, "id -u")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rendered) != "0" {
		h.prefix = "sudo -n "
	}
	h.knows = true
	return h.prefix, nil
}

func (h *Host) stream(ctx context.Context, command string, stdin io.Reader, elevation string) (session.Result, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return session.Result{}, err
	}
	h.remember(live.Destination().Principal())
	if elevation != "" {
		command = elevation + "sh -c " + quoted(command)
	}
	return live.Stream(ctx, command, stdin)
}

func (h *Host) granted(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	h.remember(live.Destination().Principal())
	elevation, err := h.rootOrSudo(ctx, live)
	if err != nil {
		return "", err
	}
	result, err := live.Stream(ctx, elevation+words(argv), stdin)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", h.refuse(what, result, elevation)
	}
	return result.Stdout, nil
}

func words(argv []string) string {
	written := make([]string, 0, len(argv))
	for _, arg := range argv {
		written = append(written, quoted(arg))
	}
	return strings.Join(written, " ")
}

type asking func(ctx context.Context, what, command string, stdin io.Reader) (string, error)

type drawing struct {
	ask   asking
	owner bool
}

func (d drawing) survey(items []Item, also ...string) string {
	if d.owner {
		return surveyOwned(items, also...)
	}
	return survey(items, also...)
}

func (h *Host) run(ctx context.Context, what, command string, stdin io.Reader) (string, error) {
	elevation, err := h.elevate(ctx)
	if err != nil {
		return "", err
	}
	return h.ran(ctx, what, command, stdin, elevation)
}

func (h *Host) reach(ctx context.Context, what, command string, stdin io.Reader) (string, error) {
	return h.ran(ctx, what, command, stdin, h.reaching(ctx))
}

func (h *Host) ran(ctx context.Context, what, command string, stdin io.Reader, elevation string) (string, error) {
	said, _, err := h.spoke(ctx, what, command, stdin, elevation)
	return said, err
}

func (h *Host) spoke(ctx context.Context, what, command string, stdin io.Reader, elevation string) (string, string, error) {
	result, err := h.stream(ctx, command, stdin, elevation)
	if err != nil {
		return "", "", err
	}
	if result.Code != 0 {
		return "", spoken(result), h.refuse(what, result, elevation)
	}
	return result.Stdout, "", nil
}

const saidLines = 4

func unstarted(code int) bool { return code == 126 || code == 127 }

func (h *Host) refuse(what string, result session.Result, elevation string) error {
	failed := refusal.CodeNotReady
	if elevation != "" && sudoRefused(result) {
		failed = refusal.CodeDenied
	}
	return refusal.Refuse(failed, "%s on %s: %s", what, h.named(), spoken(result))
}

var sudoRefusals = []string{"sudo: a password is required", "is not in the sudoers file", "is not allowed to execute"}

func sudoRefused(result session.Result) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(result.Stderr), "\n")
	return result.Code == 1 && slices.ContainsFunc(sudoRefusals, func(refusal string) bool { return strings.Contains(first, refusal) })
}

func spoken(result session.Result) string {
	said := strings.TrimSpace(result.Stderr)
	if said == "" && unstarted(result.Code) {
		said = strings.TrimSpace(result.Stdout)
	}
	if said == "" {
		return "no reason given"
	}
	if lines := strings.Split(said, "\n"); len(lines) > saidLines {
		said = strings.Join(lines[:saidLines], "\n")
	}
	return said
}

func (h *Host) Install(ctx context.Context, item Item) error {
	_, err := h.run(ctx, "write "+item.ID(), item.command(), item.stdin())
	return err
}

func (h *Host) Reassert(ctx context.Context, item Item) error {
	_, err := h.reach(ctx, "write "+item.ID(), item.command(), item.stdin())
	return err
}

func (h *Host) remove(ctx context.Context, taken removal) (bool, error) {
	switch taken.kind {
	case KindUser:
		_, err := h.run(ctx, "remove "+taken.kind+" "+taken.path, "userdel -f "+quoted(taken.path), nil)
		return err == nil, err
	case KindNetwork:
		rendered, err := h.run(ctx, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return strings.TrimSpace(rendered) != networkHeld, err
	case KindContainer, KindApps, KindResourceVolumes, KindAppNetworks:
		_, err := h.run(ctx, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return err == nil, err
	case KindUnit:
		if taken.path != LiveService && taken.path != LiveSocketUnit && taken.path != BackupsTimer {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%s %s is not ocel's to remove", taken.kind, taken.path)
		}
		_, err := h.run(ctx, "remove "+taken.kind+" "+taken.path, taken.command(), nil)
		return err == nil, err
	case KindDir, KindFile, KindSealKey, KindProxyConfig, KindRoutingTable:
		if !strings.HasPrefix(taken.path, "/") {
			return false, refusal.Refuse(refusal.CodeInvalid,
				"%q is not an absolute path", taken.path)
		}
		rendered, err := h.run(ctx, "remove "+taken.path, taken.command(), nil)
		if taken.kind == KindDir && taken.shared {
			return strings.TrimSpace(rendered) != dirHeld, err
		}
		return err == nil, err
	default:
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s %s is not ocel's to remove", taken.kind, taken.path)
	}
}

type Reading struct {
	Class    edge.Class
	Present  bool
	Keys     []byte
	Arch     string
	Stamp    Stamp
	Seal     Seal
	Observed map[string]string
	Front    Front
	Engine   Engine

	recorded    []Item
	unelevated  bool
	rerendering bool
}

func (r Reading) current(item Item) bool { return r.Observed[item.ID()] == item.Digest() }

func (r Reading) standing(kind, path string) bool {
	_, held := r.Observed[kind+" "+path]
	return held
}

func (r Reading) unfinished() bool { return r.Present && r.Stamp.State != StateComplete }

func (r Reading) Items() []Item {
	return append(Items(r.Class, r.Keys, r.Arch, r.Front), r.recorded...)
}

func (r Reading) settled() bool {
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

func (h *Host) Read(ctx context.Context, class edge.Class) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.read(ctx, class, keys, drawing{ask: h.run})
}

func (h *Host) Observe(ctx context.Context, class edge.Class) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	_, denied := h.elevate(ctx)
	read, err := h.read(ctx, class, keys, drawing{ask: h.reach})
	if err != nil {
		return Reading{}, err
	}
	read.unelevated = denied != nil
	return read, nil
}

func (h *Host) Own(ctx context.Context, class edge.Class) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.read(ctx, class, keys, drawing{ask: h.reach, owner: true})
}

func (h *Host) Survey(ctx context.Context, class edge.Class) (Reading, error) {
	return h.observing(ctx, class, nil, drawing{ask: h.run})
}

func (h *Host) observing(ctx context.Context, class edge.Class, keys []byte, drawn drawing) (Reading, error) {
	arch, err := h.arch(ctx)
	if err != nil {
		arch = ArchAMD64
	}
	return h.surveyed(ctx, class, keys, arch, drawn)
}

func (h *Host) observe(ctx context.Context, class edge.Class, keys []byte, drawn drawing) (Reading, error) {
	arch, err := h.arch(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.surveyed(ctx, class, keys, arch, drawn)
}

func (h *Host) surveyed(ctx context.Context, class edge.Class, keys []byte, arch string, drawn drawing) (Reading, error) {
	surveying := Items(class, keys, arch, h.proxyOption)
	if h.proxyOption.adopted() {
		surveying = append(surveying, frontProxy().item(""))
	}
	rendered, err := drawn.ask(ctx, "survey what "+string(class)+" holds", drawn.survey(surveying, StampPath(class), FrontRecordPath), nil)
	if err != nil {
		return Reading{}, err
	}
	observed, held, err := readSurvey(rendered)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Class: class, Keys: keys, Arch: arch, Seal: held, Observed: observed, Front: h.proxyOption, Engine: readEngine(rendered)}, nil
}

func (h *Host) read(ctx context.Context, class edge.Class, keys []byte, drawn drawing) (Reading, error) {
	read, err := h.observe(ctx, class, keys, drawn)
	if err != nil {
		return Reading{}, err
	}
	if _, stamped := read.Observed[KindFile+" "+StampPath(class)]; !stamped {
		return read, nil
	}
	stamp, err := h.readStamp(ctx, class, drawn.ask)
	if err != nil {
		return Reading{}, err
	}
	read.Present, read.Stamp = true, stamp
	return read, nil
}

func (h *Host) readStamp(ctx context.Context, class edge.Class, ask asking) (Stamp, error) {
	rendered, err := ask(ctx, "read the stamp", "cat "+quoted(StampPath(class)), nil)
	if err != nil {
		return Stamp{}, err
	}
	var stamp Stamp
	if err := json.Unmarshal([]byte(rendered), &stamp); err != nil {
		return Stamp{}, refusal.Refuse(refusal.CodeInvalid,
			"%s is not a stamp this ocel can read: %s", StampPath(class), err)
	}
	return stamp, nil
}

func (h *Host) Stamp(ctx context.Context, class edge.Class, stamp Stamp) error {
	item, err := stamp.item(class)
	if err != nil {
		return err
	}
	return h.Install(ctx, item)
}

func survey(items []Item, also ...string) string {
	return surveying(`[ -f "$p" ]`, items, also...)
}

func surveyOwned(items []Item, also ...string) string {
	return surveying(`[ -f "$p" ] && [ -r "$p" ]`, items, also...)
}

func surveying(file string, items []Item, also ...string) string {
	var probes, script strings.Builder
	script.WriteString("for p in")
	for _, item := range items {
		if probe := item.probe(); probe != "" {
			probes.WriteString(probe + "\n")
			continue
		}
		script.WriteString(" " + quoted(item.Name))
	}
	for _, path := range also {
		script.WriteString(" " + quoted(path))
	}
	stated, held := `"$(stat -c %a "$p")"`, `"$(stat -c %U "$p")"`
	script.WriteString(`; do
if [ -h "$p" ]; then ` + reports(quoted(kindLink), `"$p"`, `0`, `''`, `"$(readlink "$p")"`) + `
elif [ -d "$p" ]; then ` + reports(quoted(KindDir), `"$p"`, stated, held, `''`) + `
elif ` + file + `; then ` + reports(quoted(KindFile), `"$p"`, stated, held, `"$(sha256sum "$p" | cut -d' ' -f1)"`) + `
fi
done`)
	return probes.String() + script.String()
}

const (
	kindLink       = "fs:link"
	kindUnreadable = "probe:unreadable"
)

func unreadable(kind, name, why string) string {
	return reports(quoted(kindUnreadable), name, "0", quoted(kind), why)
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
	var held Seal
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
			held = Seal{Fingerprint: columns[4], Algorithm: SealAlgorithm, CreatedAt: columns[5]}
			content = ""
		}
		observed[columns[0]+" "+columns[1]] = digest(columns[0], columns[1], parsed, columns[3], content)
	}
	return observed, held, nil
}
