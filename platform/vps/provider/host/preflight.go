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

func (h *Host) CheckEngine(ctx context.Context) error {
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

func (h *Host) CheckDisk(ctx context.Context, repositories []string) error {
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

func (h *Host) CheckProxy(ctx context.Context) error {
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
	state := strings.TrimSpace(h.said(ctx, stateCommand(asked.name), elevation))
	if stateField(state, "Status") == "" && asked.name == SwitchboardContainer {
		return h.restoreSwitchboard(ctx, elevation)
	}
	if err := h.containerTrouble(asked.name, state); err != nil {
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

func (h *Host) containerTrouble(name, state string) error {
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
	trouble, holder := h.ownProxyTrouble, caddy.Container
	if h.proxyOption.adopted() {
		trouble, holder = h.yourProxyTrouble, "your proxy"
	}
	found, err := trouble(ctx)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s must hold ports %s: %s",
		holder, strings.Join(proxyServing(), " and "), strings.Join(found, "; "))
}

func (h *Host) ownProxyTrouble(ctx context.Context) ([]string, error) {
	ports, err := servingHeld(ctx, h.Publishing, h.Listening)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, held := range ports {
		switch {
		case len(held.containers) > 0:
			found = append(found, fmt.Sprintf("port %s is published by %s; stop it or move it off %s",
				held.port, strings.Join(held.containers, ", "), held.port))
		case held.ours:
		case len(held.bound) > 0:
			found = append(found, fmt.Sprintf("port %s is bound outside docker at %s; stop it and run `ocel bootstrap %s`",
				held.port, strings.Join(listeners.Lines(held.bound), ", "), providerkit.ClassProduction))
		default:
			found = append(found, fmt.Sprintf("nothing holds port %s; run `ocel bootstrap %s`",
				held.port, providerkit.ClassProduction))
		}
	}
	return found, nil
}

func (h *Host) yourProxyTrouble(ctx context.Context) ([]string, error) {
	var found []string
	for _, port := range proxyServing() {
		held, err := manual.PortHeld(ctx, frontBox{h}, port)
		if err != nil {
			return nil, err
		}
		if held.Trouble != "" {
			found = append(found, held.Trouble+"; "+held.Fix)
		}
	}
	return found, nil
}

type servingPort struct {
	port       string
	ours       bool
	containers []string
	bound      []listeners.Listener
}

func servingHeld(ctx context.Context,
	published func(context.Context, string) ([]string, error),
	listening func(context.Context) ([]listeners.Listener, error),
) ([]servingPort, error) {
	var held []servingPort
	var bound []listeners.Listener
	listened := false
	for _, port := range proxyServing() {
		named, err := published(ctx, port)
		if err != nil {
			return nil, err
		}
		one := servingPort{port: port, ours: slices.Contains(named, caddy.Container)}
		one.containers = slices.DeleteFunc(named, func(name string) bool { return name == caddy.Container })
		if !one.ours && len(one.containers) == 0 {
			if !listened {
				if bound, err = listening(ctx); err != nil {
					return nil, err
				}
				listened = true
			}
			one.bound = listeners.On(bound, portNumber(port))
		}
		held = append(held, one)
	}
	return held, nil
}

func portNumber(port string) int {
	number, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return number
}

const behindYourOwnProxyDocs = "https://ocel.dev/docs/providers/vps#behind-your-own-proxy"

type portHolder struct {
	name      string
	container bool
	ports     []string
}

func (p portHolder) holds() string {
	ports := make([]string, 0, len(p.ports))
	for _, port := range p.ports {
		ports = append(ports, ":"+port)
	}
	if p.container {
		return "container " + p.name + " publishes " + strings.Join(ports, " and ")
	}
	return p.name + " holds " + strings.Join(ports, " and ")
}

func withHolder(held []portHolder, name string, container bool, port string) []portHolder {
	for at := range held {
		if held[at].name == name && held[at].container == container {
			held[at].ports = append(held[at].ports, port)
			return held
		}
	}
	return append(held, portHolder{name: name, container: container, ports: []string{port}})
}

func (h *Host) servingFree(ctx context.Context, read Reading) error {
	if h.proxyOption.adopted() {
		return nil
	}
	elevation, err := h.elevate(ctx)
	if err != nil {
		return err
	}
	published := func(context.Context, string) ([]string, error) { return nil, nil }
	if read.standing(KindEngine, dockerEngine) {
		published = func(ctx context.Context, port string) ([]string, error) {
			said, err := h.ran(ctx, "ask which container publishes port "+port, words(publishing(port))+" 2>/dev/null || true", nil, elevation)
			return publishers(said), err
		}
	}
	ports, err := servingHeld(ctx, published, func(ctx context.Context) ([]listeners.Listener, error) {
		return h.portHolders(ctx, elevation)
	})
	if err != nil {
		return err
	}
	var held []portHolder
	for _, port := range ports {
		for _, name := range port.containers {
			held = withHolder(held, name, true, port.port)
		}
		names := listeners.Holders(port.bound)
		if len(port.bound) > 0 && len(names) == 0 {
			names = []string{"the process at " + strings.Join(listeners.Lines(port.bound), ", ")}
		}
		for _, name := range names {
			held = withHolder(held, name, false, port.port)
		}
	}
	if len(held) == 0 {
		return nil
	}
	holds, names, freed := make([]string, 0, len(held)), make([]string, 0, len(held)), []string{}
	var stopped []string
	for _, holder := range held {
		holds = append(holds, holder.holds())
		names = append(names, holder.name)
		if holder.container {
			freed = append(freed, "run `docker rm -f "+holder.name+"`")
			continue
		}
		stopped = append(stopped, holder.name)
	}
	if len(stopped) > 0 {
		freed = append([]string{"stop " + strings.Join(stopped, " and ")}, freed...)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s, where ocel's own proxy serves\n"+
			"Add `\"proxy\": \"manual\"` to this project's vps options and route to ocel from %s, or %s and run `%s`\n"+
			"See %s",
		strings.Join(holds, " and "), strings.Join(names, " and "), strings.Join(freed, ", "),
		providerkit.BootstrapCommand(read.Class), behindYourOwnProxyDocs)
}
