package host

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type Dial func(ctx context.Context) (*session.Session, error)

type Host struct {
	dial Dial

	elevating sync.Mutex
	settled   bool
	elevation string

	mu        sync.Mutex
	principal string
	tiers     map[providerkit.Class]bool
}

func New(dial Dial) *Host { return &Host{dial: dial, tiers: map[providerkit.Class]bool{}} }

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
	_, err := h.run(ctx, "write "+item.ID(), item.command(), item.Content)
	return err
}

func (h *Host) Remove(ctx context.Context, path string) error {
	_, err := h.run(ctx, "remove "+path, "rm -rf "+quoted(path), nil)
	return err
}

type Reading struct {
	Class    providerkit.Class
	Present  bool
	Stamp    Stamp
	Observed map[string]string
}

func (r Reading) current(item Item) bool { return r.Observed[item.ID()] == item.Digest() }

func (r Reading) standing(kind, path string) bool {
	_, held := r.Observed[kind+" "+path]
	return held
}

func (r Reading) settled() bool {
	items := Items(r.Class)
	if !r.Present || r.Stamp.State != StateComplete || !r.Stamp.records(items) {
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
	items := Items(class)
	paths := make([]string, 0, len(items)+1)
	for _, item := range items {
		paths = append(paths, item.Name)
	}
	paths = append(paths, StampPath(class))

	rendered, err := h.run(ctx, "survey what "+string(class)+" holds", survey(paths), nil)
	if err != nil {
		return Reading{}, err
	}
	observed, err := readSurvey(rendered)
	if err != nil {
		return Reading{}, err
	}

	read := Reading{Class: class, Observed: observed}
	if _, stamped := observed[KindFile+" "+StampPath(class)]; !stamped {
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

func survey(paths []string) string {
	var script strings.Builder
	script.WriteString("for p in")
	for _, path := range paths {
		script.WriteString(" " + quoted(path))
	}
	script.WriteString(`; do
if [ -d "$p" ]; then printf '%s\t%s\t%s\t%s\t\n' ` + quoted(KindDir) + ` "$p" "$(stat -c %a "$p")" "$(stat -c %U "$p")"
elif [ -f "$p" ]; then printf '%s\t%s\t%s\t%s\t%s\n' ` + quoted(KindFile) + ` "$p" "$(stat -c %a "$p")" "$(stat -c %U "$p")" "$(sha256sum "$p" | cut -d' ' -f1)"
fi
done`)
	return script.String()
}

func readSurvey(rendered string) (map[string]string, error) {
	observed := make(map[string]string)
	for _, line := range strings.Split(strings.Trim(rendered, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		columns := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(columns) != 5 {
			return nil, providerkit.Refuse(providerkit.CodeDenied,
				"the host answered a survey line ocel cannot read: %q", line)
		}
		parsed, err := mode(columns[2])
		if err != nil {
			return nil, providerkit.Refuse(providerkit.CodeDenied,
				"the host reported %q as the mode of %s, which is no mode", columns[2], columns[1])
		}
		observed[columns[0]+" "+columns[1]] = digest(columns[0], columns[1], parsed, columns[3], columns[4])
	}
	return observed, nil
}
