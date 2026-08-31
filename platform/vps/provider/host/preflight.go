package host

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
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
			"%s is refused by this host's docker socket, and every byte of the image this deploy built crosses it: %s\n"+
				"Learning this during the transfer surfaces as a failed image load halfway through. Put the login in the %q group — `ocel bootstrap %s` does it for %s — then run this again",
			h.named(), spoken(result), dockerGroup, providerkit.ClassProduction, deployUser)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s cannot run docker, and this deploy has nothing to load its image into: %s\n"+
			"Run `ocel bootstrap %s` to install the engine, or install it yourself, then run this again",
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

func (r Held) Needs() int64 {
	if r.Count == 0 {
		return FirstDeployFloor
	}
	slots := max(KeepWindow-r.Count, 0) + 1
	return r.Largest * int64(slots)
}

func (h Headroom) Needs() int64 {
	var wanted int64
	for _, held := range h.Repos {
		wanted += held.Needs()
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
			"held=$(docker image ls --filter reference=" + quoted(repository+":*") + " --format '{{.ID}}' | sort -u)\n" +
			"if [ -n \"$held\" ]; then docker image inspect --format 'size={{.Size}}' $held; fi\n")
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
			size, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
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

func unread(what, said string) error {
	return providerkit.Refuse(providerkit.CodeNotReady,
		"this host answered %q for %s, and a deploy that cannot read what a disk has left refuses rather than fills it mid-transfer", said, what)
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
		"%s holds %s free on %s and this deploy wants %s: %s\n"+
			"A disk that fills while an image is streaming fails mid-transfer and leaves the next reconcile to sweep what landed, so this refuses rather than warns. "+
			"Free space on %s, or narrow what this box keeps, then run this again",
		h.named(), sized(room.Free), room.Root, sized(wanted), arithmetic(room), room.Root)
}

func arithmetic(room Headroom) string {
	written := make([]string, 0, len(room.Repos))
	for _, repository := range slices.Sorted(keys(room.Repos)) {
		held := room.Repos[repository]
		if held.Count == 0 {
			written = append(written, fmt.Sprintf(
				"nothing is held under %s yet, so the incoming image's size cannot be extrapolated and %s is a guessed constant rather than a measurement",
				repository, sized(FirstDeployFloor)))
			continue
		}
		written = append(written, fmt.Sprintf(
			"the largest of the %d image(s) held under %s is %s, and this box keeps %d, so %d unfilled slot(s) plus the incoming one wants %s",
			held.Count, repository, sized(held.Largest), KeepWindow, max(KeepWindow-held.Count, 0), sized(held.Needs())))
	}
	return strings.Join(written, "; ")
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
	result, err := h.stream(ctx, words(helperCommand("upstreams")), nil, elevation)
	if err != nil {
		return err
	}
	if result.Code == 0 {
		return nil
	}
	return h.proxyTrouble(ctx, elevation, spoken(result))
}

const (
	proxyExited     = "exited"
	proxyRestarting = "restarting"
)

func (h *Host) proxyTrouble(ctx context.Context, elevation, said string) error {
	state := strings.TrimSpace(h.said(ctx, stateCommand(ProxyContainer), elevation))
	status := stateField(state, "Status")
	switch {
	case status == "":
		return providerkit.Refuse(providerkit.CodeNotReady,
			"no container named %s stands on %s, and every request this deploy's app would answer arrives through it: %s\n"+
				"Run `ocel bootstrap %s` to put the proxy back, then run this again",
			ProxyContainer, h.named(), state, providerkit.ClassProduction)
	case status == proxyRestarting:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s is restarting rather than running, so it is coming up and falling over rather than standing still: %s\n"+
				"Read `docker logs %s` for why it will not stay up, then run this again",
			ProxyContainer, h.named(), state, ProxyContainer)
	case status == proxyExited:
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s has exited and stayed exited, and deploying into a box whose proxy is down stands a container up that nothing routes to and reports a green deploy: %s\n"+
				"Run `docker start %s`, or `ocel bootstrap %s` to write it back, then run this again",
			ProxyContainer, h.named(), state, ProxyContainer, providerkit.ClassProduction)
	case status != "running":
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s on %s is %s rather than running, and deploying into a box whose proxy is down stands a container up that nothing routes to and reports a green deploy: %s\n"+
				"Run `docker start %s`, or `ocel bootstrap %s` to write it back, then run this again",
			ProxyContainer, h.named(), status, state, ProxyContainer, providerkit.ClassProduction)
	}

	if _, err := h.ran(ctx, "ask whether the proxy's flip helper stands",
		words(execCommand("test", "-x", ProxyHelperMount)), nil, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s is running on %s and carries no executable flip helper at %s, which is a bootstrap ocel never finished rather than a proxy that is down: %v\n"+
				"Run `ocel bootstrap %s` to write the helper back, then run this again",
			ProxyContainer, h.named(), ProxyHelperMount, err, providerkit.ClassProduction)
	}
	if _, err := h.ran(ctx, "ask whether the proxy's admin socket stands",
		words(execCommand("test", "-S", ProxyAdminSocket)), nil, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s is running on %s and there is no socket at %s, so the proxy never opened the admin endpoint this deploy flips it through: %v\n"+
				"Run `ocel bootstrap %s` to write back the configuration that binds it, then run this again",
			ProxyContainer, h.named(), ProxyAdminSocket, err, providerkit.ClassProduction)
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"%s is running on %s and %s is there, and the admin endpoint refused the one read this deploy makes of it: %s\n"+
			"The socket's permissions are the whole of its access control, so check it stands at %s owned by root, then run this again",
		ProxyContainer, h.named(), ProxyAdminSocket, said, caddySocketMode)
}

const caddySocketMode = "0600"

func execCommand(argv ...string) []string {
	return append([]string{"docker", "exec", ProxyContainer}, argv...)
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
	var found []string
	for _, port := range proxyServing() {
		refusal, err := h.portHeld(ctx, port)
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
		"%s serves every request on this host through %s, and on a bootstrapped box ports %s are taken by it rather than free: %s",
		ProxyContainer, ProxyContainer, strings.Join(proxyServing(), " and "), strings.Join(found, "; "))
}

func (h *Host) portHeld(ctx context.Context, port string) (string, error) {
	named, err := h.Publishing(ctx, port)
	if err != nil {
		return "", err
	}
	foreign := slices.DeleteFunc(slices.Clone(named), func(name string) bool { return name == ProxyContainer })
	if len(foreign) > 0 {
		return fmt.Sprintf("port %s on this host is published by %s, which is not %s, so this deploy's routes would be written into a proxy nothing reaches — stop %s or move it off %s, then run this again",
			port, strings.Join(foreign, ", "), ProxyContainer, strings.Join(foreign, ", "), port), nil
	}
	if slices.Contains(named, ProxyContainer) {
		return "", nil
	}
	held, err := h.Listening(ctx)
	if err != nil {
		return "", err
	}
	if bound := listeners.On(held, portNumber(port)); len(bound) > 0 {
		return fmt.Sprintf("port %s on this host is bound at %s by something outside this engine and no container publishes it, so %s never took it — stop whatever holds it and run `ocel bootstrap %s`",
			port, strings.Join(listeners.Lines(bound), ", "), ProxyContainer, providerkit.ClassProduction), nil
	}
	return fmt.Sprintf("nothing on this host holds port %s, so %s is not serving it — run `ocel bootstrap %s` to put the proxy back",
		port, ProxyContainer, providerkit.ClassProduction), nil
}

func portNumber(port string) int {
	number, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return number
}
