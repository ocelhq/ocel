package host

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

const KeepWindow = 3

const FirstDeployFloor = 2 << 30

func (h *Host) EngineStanding(ctx context.Context) error {
	result, err := h.stream(ctx, dockerReach+" >/dev/null", nil, "")
	if err != nil {
		return err
	}
	if result.Code == 0 {
		return nil
	}
	if deniedSocket(result.Stderr) {
		return providerkit.Refuse(providerkit.CodeDenied,
			"%s is refused by the docker socket: %s\n"+
				"Add the login to the %q group; `ocel bootstrap %s` does it for %s",
			h.named(), spoken(result), dockerGroup, providerkit.ClassProduction, deployUser)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s cannot run docker: %s\n"+
			"Run `ocel bootstrap %s` or install docker",
		h.named(), spoken(result), providerkit.ClassProduction)
}

type Held struct {
	Largest int64
	Count   int
}

type Headroom struct {
	Root  string
	Free  int64
	Repos map[string]Held
}

func (r Held) Measured() int64 {
	slots := max(KeepWindow-r.Count, 0) + 1
	return r.Largest * int64(slots)
}

func (r Held) Needs() int64 { return max(FirstDeployFloor, r.Measured()) }

func (h Headroom) Needs() int64 {
	var wanted int64
	for _, held := range h.Repos {
		wanted += held.Needs() + LogCeiling
	}
	return wanted
}

func headroomCommand(repositories []string) string {
	written := strings.Builder{}
	written.WriteString("set -e\n" +
		"root=$(docker info --format '{{.DockerRootDir}}')\n" +
		"printf 'root=%s\\n' \"$root\"\n" +
		"printf 'free=%s\\n' \"$(df -Pk \"$root\" | tail -n 1 | awk '{print $4}')\"\n")
	for _, repository := range repositories {
		written.WriteString("printf 'repo=%s\\n' " + quoted(repository) + "\n" +
			"docker image ls --filter reference=" + quoted(repository+":*") + " --format '{{.ID}} {{.Size}}' |" +
			" sort -u | sed 's/^[^ ]* /size=/'\n")
	}
	return written.String()
}

func readHeadroom(rendered string) (Headroom, error) {
	room := Headroom{Repos: map[string]Held{}}
	repository := ""
	for line := range strings.Lines(rendered) {
		key, value, split := strings.Cut(strings.TrimSpace(line), "=")
		if !split {
			continue
		}
		switch key {
		case "root":
			room.Root = value
		case "free":
			free, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Headroom{}, unread("the space free on the docker data root", value)
			}
			room.Free = free * 1024
		case "repo":
			repository = value
			room.Repos[repository] = Held{}
		case "size":
			size, read := occupied(value)
			if !read {
				return Headroom{}, unread("the size of an image held under "+repository, value)
			}
			held := room.Repos[repository]
			held.Count++
			held.Largest = max(held.Largest, size)
			room.Repos[repository] = held
		}
	}
	if room.Root == "" || room.Free == 0 {
		return Headroom{}, unread("the docker data root and what is free on it", strings.TrimSpace(rendered))
	}
	return room, nil
}

var occupation = map[string]int64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15}

func occupied(said string) (int64, bool) {
	cut := strings.IndexFunc(said, func(r rune) bool { return r != '.' && (r < '0' || r > '9') })
	if cut <= 0 {
		return 0, false
	}
	scale, held := occupation[said[cut:]]
	if !held {
		return 0, false
	}
	value, err := strconv.ParseFloat(said[:cut], 64)
	if err != nil {
		return 0, false
	}
	return int64(math.Round(value * float64(scale))), true
}

func unread(what, said string) error {
	return providerkit.Refuse(providerkit.CodeNotReady,
		"this host answered %q for %s", said, what)
}

func (h *Host) Headroom(ctx context.Context, repositories []string) (Headroom, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return Headroom{}, err
	}
	rendered, err := h.ran(ctx, "read what is free on this host's docker data root", headroomCommand(repositories), nil, elevation)
	if err != nil {
		return Headroom{}, err
	}
	return readHeadroom(rendered)
}

func (h *Host) DiskStanding(ctx context.Context, repositories []string) error {
	if len(repositories) == 0 {
		return nil
	}
	room, err := h.Headroom(ctx, repositories)
	if err != nil {
		return err
	}
	wanted := room.Needs()
	if room.Free >= wanted {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s has %s free on %s; this deploy needs %s: %s\n"+
			"Free space on %s",
		h.named(), sized(room.Free), room.Root, sized(wanted), arithmetic(room), room.Root)
}

func arithmetic(room Headroom) string {
	written := make([]string, 0, len(room.Repos))
	for _, repository := range slices.Sorted(keys(room.Repos)) {
		held := room.Repos[repository]
		if held.Measured() >= FirstDeployFloor {
			written = append(written, measured(repository, held))
			continue
		}
		written = append(written, fmt.Sprintf(
			"%s (image size unknown, guessed %s)",
			measured(repository, held), sized(FirstDeployFloor)))
	}
	return strings.Join(written, "; ") + fmt.Sprintf("; plus %s of logs per container", sized(LogCeiling))
}

func measured(repository string, held Held) string {
	if held.Count == 0 {
		return repository + ": nothing held yet"
	}
	return fmt.Sprintf(
		"%s: %d image(s), largest %s, keeping %d, so %d empty slot(s) + incoming = %s",
		repository, held.Count, sized(held.Largest), KeepWindow, max(KeepWindow-held.Count, 0), sized(held.Measured()))
}

func keys(held map[string]Held) func(func(string) bool) {
	return func(yield func(string) bool) {
		for name := range held {
			if !yield(name) {
				return
			}
		}
	}
}

const unit = 1024

func sized(count int64) string {
	if count < unit {
		return strconv.FormatInt(count, 10) + " B"
	}
	value, scale := float64(count), ""
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		scale = suffix
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, scale)
}

func (h *Host) ProxyStanding(ctx context.Context) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	asking := []boxContainer{switchboardStanding(nil, h.proxyOption)}
	if !h.proxyOption.adopted() {
		asking = append(asking, frontProxy())
	}
	for _, asked := range asking {
		if err := h.answers(ctx, asked, elevation); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) answers(ctx context.Context, asked boxContainer, elevation string) error {
	result, err := h.stream(ctx, words(asked.readiness()), nil, elevation)
	if err != nil {
		return err
	}
	if result.Code == 0 {
		return nil
	}
	if err := h.containerTrouble(ctx, asked.name, elevation); err != nil {
		return err
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s on %s %s: %s\n"+
			"Run `ocel bootstrap %s`",
		asked.name, h.named(), asked.unready, spoken(result), providerkit.ClassProduction)
}

const (
	proxyExited     = "exited"
	proxyRestarting = "restarting"
)

func (h *Host) containerTrouble(ctx context.Context, name, elevation string) error {
	state := strings.TrimSpace(h.said(ctx, stateCommand(name), elevation))
	status := stateField(state, "Status")
	switch {
	case status == "":
		return providerkit.Refuse(providerkit.CodeNotReady,
			"no %s container on %s: %s\n"+
				"Run `ocel bootstrap %s`",
			name, h.named(), state, providerkit.ClassProduction)
	case status == proxyRestarting:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s keeps restarting: %s\n"+
				"Check `docker logs %s`",
			name, h.named(), state, name)
	case status == proxyExited:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s has exited: %s\n"+
				"Run `docker start %s` or `ocel bootstrap %s`",
			name, h.named(), state, name, providerkit.ClassProduction)
	case status != "running":
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s is %s, not running: %s\n"+
				"Run `docker start %s` or `ocel bootstrap %s`",
			name, h.named(), status, state, name, providerkit.ClassProduction)
	}
	return nil
}

func stateField(state, label string) string {
	for _, field := range strings.Fields(state) {
		if name, value, split := strings.Cut(field, "="); split && name == label {
			return value
		}
	}
	return ""
}

func (h *Host) ServingPortsHeld(ctx context.Context) error {
	held := h.portHeld
	holder := caddy.Container
	if h.proxyOption.adopted() {
		held, holder = h.portAdopted, "your proxy"
	}
	var found []string
	for _, port := range proxyServing() {
		refusal, err := held(ctx, port)
		if err != nil {
			return err
		}
		if refusal != "" {
			found = append(found, refusal)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s must hold ports %s: %s",
		holder, strings.Join(proxyServing(), " and "), strings.Join(found, "; "))
}

func (h *Host) portHeld(ctx context.Context, port string) (string, error) {
	named, err := h.Publishing(ctx, port)
	if err != nil {
		return "", err
	}
	foreign := slices.DeleteFunc(slices.Clone(named), func(name string) bool { return name == caddy.Container })
	if len(foreign) > 0 {
		return fmt.Sprintf("port %s is published by %s; stop it or move it off %s",
			port, strings.Join(foreign, ", "), port), nil
	}
	if slices.Contains(named, caddy.Container) {
		return "", nil
	}
	held, err := h.Listening(ctx)
	if err != nil {
		return "", err
	}
	if bound := listeners.On(held, portNumber(port)); len(bound) > 0 {
		return fmt.Sprintf("port %s is bound outside docker at %s; stop it and run `ocel bootstrap %s`",
			port, strings.Join(listeners.Lines(bound), ", "), providerkit.ClassProduction), nil
	}
	return fmt.Sprintf("nothing holds port %s; run `ocel bootstrap %s`",
		port, providerkit.ClassProduction), nil
}

func (h *Host) portAdopted(ctx context.Context, port string) (string, error) {
	held, err := manual.PortHeld(ctx, frontBox{h}, port)
	if err != nil || held.Trouble == "" {
		return "", err
	}
	return held.Trouble + "; " + held.Fix, nil
}

func portNumber(port string) int {
	number, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return number
}
