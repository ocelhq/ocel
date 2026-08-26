package host

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type Conn interface {
	Exec(ctx context.Context, command string, stdin []byte) (session.Result, error)
	Run(ctx context.Context, command string) (string, error)
	Preflight(ctx context.Context) (session.Facts, error)
	Destination() session.Destination
}

type Dial func(ctx context.Context) (Conn, error)

type Host struct {
	dial   Dial
	deploy Keys

	elevating sync.Mutex
	settled   bool
	elevation string

	rooting sync.Mutex
	knows   bool
	prefix  string

	holding sync.Mutex
	held    []byte

	mu        sync.Mutex
	principal string
	tiers     map[providerkit.Class]bool
}

func New(dial Dial, deploy Keys) *Host {
	return &Host{dial: dial, deploy: deploy, tiers: map[providerkit.Class]bool{}}
}

func (h *Host) holds(ctx context.Context, class providerkit.Class) (bool, error) {
	h.mu.Lock()
	stood := h.tiers[class]
	h.mu.Unlock()
	if stood {
		return true, nil
	}
	rendered, err := h.run(ctx, "ask where "+string(class)+" keeps its records",
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
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	facts, err := live.Preflight(ctx)
	if err != nil {
		return "", err
	}
	if !facts.Root {
		h.elevation = "sudo -n "
	}
	h.settled = true
	return h.elevation, nil
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

func (h *Host) exec(ctx context.Context, command string, stdin []byte) (session.Result, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return session.Result{}, err
	}
	h.remember(live.Destination().Principal())
	elevation, err := h.elevate(ctx)
	if err != nil {
		return session.Result{}, err
	}
	if elevation != "" {
		command = elevation + "sh -c " + quoted(command)
	}
	return live.Exec(ctx, command, stdin)
}

func (h *Host) granted(ctx context.Context, what string, argv []string, stdin []byte) (string, error) {
	live, err := h.dial(ctx)
	if err != nil {
		return "", err
	}
	h.remember(live.Destination().Principal())
	elevation, err := h.rootOrSudo(ctx, live)
	if err != nil {
		return "", err
	}
	result, err := live.Exec(ctx, elevation+words(argv), stdin)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", h.refuse(what, result)
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

func (h *Host) run(ctx context.Context, what, command string, stdin []byte) (string, error) {
	result, err := h.exec(ctx, command, stdin)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", h.refuse(what, result)
	}
	return result.Stdout, nil
}

func (h *Host) refuse(what string, result session.Result) error {
	said := strings.TrimSpace(result.Stderr)
	if said == "" {
		said = "it said nothing about why"
	}
	return providerkit.Refuse(providerkit.CodeDenied, "%s on %s: %s", what, h.named(), said)
}

func (h *Host) Install(ctx context.Context, item Item) error {
	_, err := h.run(ctx, "write "+item.ID(), item.command(), item.stdin())
	return err
}

func (h *Host) Remove(ctx context.Context, kind, name string) error {
	switch kind {
	case KindUser:
		_, err := h.run(ctx, "remove "+kind+" "+name, "userdel -f "+quoted(name), nil)
		return err
	case KindDir, KindFile, KindSealKey:
		if !strings.HasPrefix(name, "/") {
			return providerkit.Refuse(providerkit.CodeInvalid,
				"%q names no path on this host, and ocel takes nothing it cannot name in full", name)
		}
		_, err := h.run(ctx, "remove "+name, "rm -rf "+quoted(name), nil)
		return err
	default:
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s %s is not ocel's to take: what this host runs stays when ocel goes", kind, name)
	}
}

type Reading struct {
	Class    providerkit.Class
	Present  bool
	Keys     []byte
	Stamp    Stamp
	Seal     Seal
	Observed map[string]string
}

func (r Reading) current(item Item) bool { return r.Observed[item.ID()] == item.Digest() }

func (r Reading) standing(kind, path string) bool {
	_, held := r.Observed[kind+" "+path]
	return held
}

func (r Reading) settled() bool {
	items := Items(r.Class, r.Keys)
	if !r.Present || r.Stamp.State != StateComplete || !r.Stamp.records(items) {
		return false
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

func (h *Host) Read(ctx context.Context, class providerkit.Class) (Reading, error) {
	keys, err := h.keys(ctx)
	if err != nil {
		return Reading{}, err
	}
	return h.read(ctx, class, keys)
}

func (h *Host) Survey(ctx context.Context, class providerkit.Class) (Reading, error) {
	return h.observe(ctx, class, nil)
}

func (h *Host) observe(ctx context.Context, class providerkit.Class, keys []byte) (Reading, error) {
	rendered, err := h.run(ctx, "survey what "+string(class)+" holds", survey(Items(class, keys), StampPath(class)), nil)
	if err != nil {
		return Reading{}, err
	}
	observed, held, err := readSurvey(rendered)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Class: class, Keys: keys, Seal: held, Observed: observed}, nil
}

func (h *Host) read(ctx context.Context, class providerkit.Class, keys []byte) (Reading, error) {
	read, err := h.observe(ctx, class, keys)
	if err != nil {
		return Reading{}, err
	}
	if _, stamped := read.Observed[KindFile+" "+StampPath(class)]; !stamped {
		return read, nil
	}
	stamp, err := h.readStamp(ctx, class)
	if err != nil {
		return Reading{}, err
	}
	read.Present, read.Stamp = true, stamp
	return read, nil
}

func (h *Host) readStamp(ctx context.Context, class providerkit.Class) (Stamp, error) {
	rendered, err := h.run(ctx, "read the stamp", "cat "+quoted(StampPath(class)), nil)
	if err != nil {
		return Stamp{}, err
	}
	var stamp Stamp
	if err := json.Unmarshal([]byte(rendered), &stamp); err != nil {
		return Stamp{}, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is not a stamp this ocel can read: %s", StampPath(class), err)
	}
	return stamp, nil
}

func (h *Host) Stamp(ctx context.Context, class providerkit.Class, stamp Stamp) error {
	item, err := stamp.item(class)
	if err != nil {
		return err
	}
	return h.Install(ctx, item)
}

func survey(items []Item, also ...string) string {
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
if [ -d "$p" ]; then ` + reports(quoted(KindDir), `"$p"`, stated, held, `''`) + `
elif [ -f "$p" ]; then ` + reports(quoted(KindFile), `"$p"`, stated, held, `"$(sha256sum "$p" | cut -d' ' -f1)"`) + `
fi
done`)
	return probes.String() + script.String()
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
		sealed := columns[0] == KindSealKey
		if (sealed && len(columns) != 6) || (!sealed && len(columns) != 5) {
			return nil, Seal{}, providerkit.Refuse(providerkit.CodeDenied,
				"the host answered a survey line ocel cannot read: %q", line)
		}
		parsed, err := mode(columns[2])
		if err != nil {
			return nil, Seal{}, providerkit.Refuse(providerkit.CodeDenied,
				"the host reported %q as the mode of %s, which is no mode", columns[2], columns[1])
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
