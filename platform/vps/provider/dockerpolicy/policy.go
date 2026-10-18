package dockerpolicy

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

type Containers interface {
	IsOcels(ctx context.Context, container string) (bool, error)
	IsHostBound(ctx context.Context, volume string) (bool, error)
}

var Capabilities = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "NET_BIND_SERVICE", "SETGID", "SETUID", "SETPCAP"}

type Policy struct {
	boxapi.DockerGrants
	StateRoot string
	Inspect   Containers
	Ancestors func(path string) error
	OwnerUID  int
	OwnerGID  int
}

type Invocation struct {
	Argv   []string
	Stdin  *os.File
	Stdout *os.File
	Extra  []*os.File
}

func (i Invocation) Close() {
	for _, file := range append([]*os.File{i.Stdin, i.Stdout}, i.Extra...) {
		if file != nil {
			_ = file.Close()
		}
	}
}

type refused struct{ reason string }

func (r refused) Error() string { return r.reason }

func refuse(format string, args ...any) error { return refused{reason: fmt.Sprintf(format, args...)} }

func IsRefused(err error) bool {
	var r refused
	return errors.As(err, &r)
}

var plain = map[string]bool{
	"version": true, "info": true, "ps": true, "events": true, "logs": true, "inspect": true,
	"start": true, "stop": true, "restart": true, "rm": true, "kill": true,
	"wait": true, "port": true, "pause": true, "unpause": true, "pull": true, "tag": true,
	"rmi": true, "images": true,
}

var plainImages = map[string]bool{"ls": true, "inspect": true, "rm": true, "tag": true, "pull": true}

var plainNetworks = map[string]bool{"inspect": true, "ls": true, "rm": true, "disconnect": true}

var plainVolumes = map[string]bool{"inspect": true, "ls": true, "rm": true}

func (p Policy) Open(ctx context.Context, argv []string) (Invocation, error) {
	if len(argv) == 0 {
		return Invocation{}, refuse("no docker command was named")
	}
	verb, rest := argv[0], argv[1:]
	switch {
	case strings.HasPrefix(verb, "-"):
		return Invocation{}, refuse("docker's own option %s is not one a deploy sets", verb)
	case plain[verb]:
		return Invocation{Argv: argv}, nil
	case verb == "run" || verb == "create":
		return p.opened(verb, rest, func() flagSet { return p.runFlags(ctx) })
	case verb == "exec":
		return p.exec(ctx, rest)
	case verb == "rename":
		return p.rename(ctx, argv)
	case verb == "load":
		return p.load([]string{verb}, rest)
	case verb == "save":
		return p.save([]string{verb}, rest)
	case verb == "image" && len(rest) > 0 && plainImages[rest[0]]:
		return Invocation{Argv: argv}, nil
	case verb == "image" && len(rest) > 0 && rest[0] == "load":
		return p.load(argv[:2], rest[1:])
	case verb == "image" && len(rest) > 0 && rest[0] == "save":
		return p.save(argv[:2], rest[1:])
	case verb == "network" && len(rest) > 0 && plainNetworks[rest[0]]:
		return Invocation{Argv: argv}, nil
	case verb == "network" && len(rest) > 0 && rest[0] == "create":
		return p.opened("network create", rest[1:], p.networkCreateFlags)
	case verb == "network" && len(rest) > 0 && rest[0] == "connect":
		return p.connect(rest[1:])
	case verb == "volume" && len(rest) > 0 && plainVolumes[rest[0]]:
		return Invocation{Argv: argv}, nil
	case verb == "volume" && len(rest) > 0 && rest[0] == "create":
		return p.opened("volume create", rest[1:], p.volumeCreateFlags)
	case verb == "system" && len(rest) > 0 && rest[0] == "df":
		return Invocation{Argv: argv}, nil
	}
	return Invocation{}, refuse("docker %s is not a command a deploy runs", strings.Join(argv[:min(len(argv), 2)], " "))
}

type flagCheck func(inv *Invocation, flag, value string) error

type flagSet struct {
	valued map[string]bool
	plain  map[string]bool
	check  flagCheck
}

func (p Policy) opened(verb string, args []string, set func() flagSet) (Invocation, error) {
	inv, operands, err := p.parse(strings.Fields(verb), args, set())
	if err != nil {
		inv.Close()
		return Invocation{}, err
	}
	switch verb {
	case "run", "create":
		if !slices.Contains(inv.Argv, "--network") && !slices.ContainsFunc(inv.Argv, func(arg string) bool { return strings.HasPrefix(arg, "--network=") }) {
			inv.Close()
			return Invocation{}, refuse("docker %s names no --network, and the default bridge reaches containers no deploy owns", verb)
		}
		if len(operands) == 0 {
			inv.Close()
			return Invocation{}, refuse("docker %s names no image", verb)
		}
	}
	return inv, nil
}

func (p Policy) parse(lead, args []string, set flagSet) (Invocation, []string, error) {
	inv := Invocation{Argv: slices.Clone(lead)}
	for at := 0; at < len(args); at++ {
		arg := args[at]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			inv.Argv = append(inv.Argv, args[at:]...)
			return inv, args[at:], nil
		}
		flag, value, inline := strings.Cut(arg, "=")
		switch {
		case set.plain[flag]:
			inv.Argv = append(inv.Argv, arg)
			continue
		case !set.valued[flag]:
			return inv, nil, refuse("%s is not an option a deploy sets", flag)
		case !inline:
			if at+1 >= len(args) {
				return inv, nil, refuse("%s names no value", flag)
			}
			at++
			value = args[at]
		}
		before := len(inv.Argv)
		if err := set.check(&inv, flag, value); err != nil {
			return inv, nil, err
		}
		if len(inv.Argv) == before {
			inv.Argv = append(inv.Argv, flag, value)
		}
	}
	return inv, nil, nil
}

func (p Policy) runFlags(ctx context.Context) flagSet {
	return flagSet{
		valued: setOf("--name", "--restart", "--network", "--label", "--log-driver", "--log-opt", "--pids-limit",
			"--security-opt", "--cap-drop", "--cap-add", "--user", "--memory", "--cpus", "--shm-size", "--env",
			"--env-file", "--mount", "--volume", "--tmpfs", "--publish", "--entrypoint"),
		plain: setOf("--detach", "-d", "--rm", "--read-only", "--interactive", "-i", "--init"),
		check: func(inv *Invocation, flag, value string) error { return p.runFlag(ctx, inv, flag, value) },
	}
}

func (p Policy) runFlag(ctx context.Context, inv *Invocation, flag, value string) error {
	switch flag {
	case "--network":
		return p.refuseNetwork(value)
	case "--cap-add":
		if !slices.Contains(Capabilities, strings.TrimPrefix(strings.ToUpper(value), "CAP_")) {
			return refuse("--cap-add %s is not a capability any container a deploy runs is handed", value)
		}
	case "--security-opt":
		if value != "no-new-privileges" && value != "no-new-privileges:true" && value != "no-new-privileges=true" {
			return refuse("--security-opt %s loosens what docker confines a container to", value)
		}
	case "--publish":
		if !strings.HasPrefix(value, "127.0.0.1:") {
			return refuse("--publish %s publishes beyond the box's loopback", value)
		}
	case "--mount":
		return p.refuseMount(ctx, value)
	case "--volume":
		return p.refuseVolume(ctx, value)
	case "--env-file":
		file, err := p.openUnder(value, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		inv.Extra = append(inv.Extra, file)
		inv.Argv = append(inv.Argv, flag, "/dev/fd/"+strconv.Itoa(2+len(inv.Extra)))
	}
	return nil
}

func (p Policy) refuseNetwork(network string) error {
	if network == "none" || network == boxapi.ProxyNetwork || strings.HasPrefix(network, "ocel-") && !strings.Contains(network, ":") ||
		slices.Contains(p.Networks, network) {
		return nil
	}
	return refuse("network %s is neither one of ocel's nor one bootstrap granted", network)
}

func (p Policy) refuseMount(ctx context.Context, spec string) error {
	fields, err := csv.NewReader(strings.NewReader(spec)).Read()
	if err != nil {
		return refuse("--mount %s is not a mount docker reads: %v", spec, err)
	}
	kind, source, readonly := "volume", "", false
	for _, field := range fields {
		key, value, _ := strings.Cut(field, "=")
		switch strings.ToLower(key) {
		case "type":
			kind = value
		case "source", "src":
			source = value
		case "readonly", "ro":
			readonly = value == "" || value == "true" || value == "1"
		case "target", "dst", "destination", "bind-propagation":
		case "volume-nocopy", "tmpfs-size", "tmpfs-mode":
		default:
			return refuse("--mount option %s is not one a deploy sets", key)
		}
	}
	switch kind {
	case "bind":
		return p.refuseBind(source, !readonly)
	case "volume":
		return p.refuseBoundVolume(ctx, source)
	case "tmpfs":
		return nil
	}
	return refuse("--mount type %s is not one a deploy mounts", kind)
}

func (p Policy) refuseVolume(ctx context.Context, spec string) error {
	source, rest, _ := strings.Cut(spec, ":")
	if !strings.HasPrefix(source, "/") {
		return p.refuseBoundVolume(ctx, source)
	}
	_, options, _ := strings.Cut(rest, ":")
	return p.refuseBind(source, !slices.Contains(strings.Split(options, ","), "ro"))
}

func (p Policy) refuseBoundVolume(ctx context.Context, volume string) error {
	if volume == "" {
		return nil
	}
	bound, err := p.Inspect.IsHostBound(ctx, volume)
	if err != nil {
		return err
	}
	if bound {
		return refuse("volume %s is a path on the box under another name", volume)
	}
	return nil
}

func (p Policy) refuseBind(source string, writable bool) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return refuse("bind source %s is not a clean absolute path", source)
	}
	at := slices.IndexFunc(p.Binds, func(bind boxapi.DockerBind) bool { return bind.Path == source })
	if at < 0 {
		return refuse("%s is not a directory bootstrap granted deploys to mount", source)
	}
	if writable && !p.Binds[at].Writable {
		return refuse("%s is granted to deploys read-only", source)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return refuse("%s cannot be mounted: %v", source, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return refuse("%s is a symbolic link, which the daemon follows wherever it points when the container starts", source)
	}
	return p.ancestors()(source)
}

func (p Policy) ancestors() func(string) error {
	if p.Ancestors != nil {
		return p.Ancestors
	}
	return func(path string) error { return RefuseAncestorsTheCallerCanChange(path, p.OwnerUID, p.OwnerGID) }
}

func RefuseAncestorsTheCallerCanChange(path string, uid, gid int) error {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return refuse("%s cannot be read: %v", parent, err)
		}
		owner, known := info.Sys().(*syscall.Stat_t)
		perm := info.Mode().Perm()
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !known || int(owner.Uid) == uid ||
			perm&0o002 != 0 || perm&0o020 != 0 && int(owner.Gid) == gid {
			return refuse("%s sits under %s, which the deploy login can change, so what the daemon mounts could be swapped", path, parent)
		}
		if parent == "/" {
			return nil
		}
	}
}

func (p Policy) exec(ctx context.Context, args []string) (Invocation, error) {
	inv, operands, err := p.parse([]string{"exec"}, args, flagSet{
		valued: setOf("--user", "-u", "--env", "-e", "--workdir", "-w"),
		plain:  setOf("--interactive", "-i", "--detach", "-d"),
		check:  func(*Invocation, string, string) error { return nil },
	})
	if err != nil {
		return Invocation{}, err
	}
	if len(operands) < 2 {
		return Invocation{}, refuse("docker exec names no container and command")
	}
	container := operands[0]
	if slices.Contains(p.Containers, container) {
		return inv, nil
	}
	ours, err := p.Inspect.IsOcels(ctx, container)
	if err != nil {
		return Invocation{}, err
	}
	if !ours {
		return Invocation{}, refuse("%s is not a container ocel runs or bootstrap granted, so a deploy runs nothing inside it", container)
	}
	return inv, nil
}

func (p Policy) rename(ctx context.Context, argv []string) (Invocation, error) {
	if len(argv) != 3 || strings.HasPrefix(argv[1], "-") || strings.HasPrefix(argv[2], "-") {
		return Invocation{}, refuse("docker rename names a container and its new name")
	}
	if slices.Contains(p.Containers, argv[2]) {
		return Invocation{}, refuse("%s is a name bootstrap granted exec into, and no container takes it by rename", argv[2])
	}
	ours, err := p.Inspect.IsOcels(ctx, argv[1])
	if err != nil {
		return Invocation{}, err
	}
	if !ours {
		return Invocation{}, refuse("%s is not a container ocel runs, so a deploy renames nothing of it", argv[1])
	}
	return Invocation{Argv: argv}, nil
}

func (p Policy) connect(args []string) (Invocation, error) {
	inv, operands, err := p.parse([]string{"network", "connect"}, args, flagSet{
		valued: setOf("--alias"),
		check:  func(*Invocation, string, string) error { return nil },
	})
	if err != nil {
		return Invocation{}, err
	}
	if len(operands) != 2 {
		return Invocation{}, refuse("docker network connect names a network and a container")
	}
	return inv, p.refuseNetwork(operands[0])
}

func (p Policy) networkCreateFlags() flagSet {
	return flagSet{
		valued: setOf("--label", "--driver", "-d"),
		plain:  setOf("--internal", "--attachable"),
		check: func(_ *Invocation, flag, value string) error {
			if (flag == "--driver" || flag == "-d") && value != "bridge" {
				return refuse("network driver %s is not the bridge a deploy creates", value)
			}
			return nil
		},
	}
}

func (p Policy) volumeCreateFlags() flagSet {
	return flagSet{
		valued: setOf("--label", "--driver", "-d", "--opt", "-o"),
		check: func(_ *Invocation, flag, value string) error {
			if flag != "--opt" && flag != "-o" {
				return nil
			}
			key, option, _ := strings.Cut(value, "=")
			switch {
			case key == "device" && strings.HasPrefix(option, "/"):
				return refuse("volume option %s mounts a path on the box", value)
			case key == "o" && slices.ContainsFunc(strings.Split(option, ","), func(part string) bool { return part == "bind" || part == "rbind" }):
				return refuse("volume option %s binds a path on the box", value)
			}
			return nil
		},
	}
}

func (p Policy) load(lead, args []string) (Invocation, error) {
	inv := Invocation{Argv: slices.Clone(lead)}
	for at := 0; at < len(args); at++ {
		flag, value, inline := strings.Cut(args[at], "=")
		switch flag {
		case "-q", "--quiet":
			inv.Argv = append(inv.Argv, args[at])
		case "-i", "--input":
			if !inline {
				if at+1 >= len(args) {
					return Invocation{}, refuse("%s names no file", flag)
				}
				at++
				value = args[at]
			}
			file, err := p.openUnder(value, os.O_RDONLY, 0)
			if err != nil {
				return Invocation{}, err
			}
			inv.Stdin = file
		default:
			inv.Close()
			return Invocation{}, refuse("%s is not an option a deploy loads with", args[at])
		}
	}
	return inv, nil
}

func (p Policy) save(lead, args []string) (Invocation, error) {
	inv := Invocation{Argv: slices.Clone(lead)}
	for at := 0; at < len(args); at++ {
		flag, value, inline := strings.Cut(args[at], "=")
		switch {
		case flag == "-o" || flag == "--output":
			if !inline {
				if at+1 >= len(args) {
					return Invocation{}, refuse("%s names no file", flag)
				}
				at++
				value = args[at]
			}
			file, err := p.openUnder(value, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
			if err != nil {
				return Invocation{}, err
			}
			if err := file.Chown(p.OwnerUID, p.OwnerGID); err != nil {
				_ = file.Close()
				return Invocation{}, err
			}
			inv.Stdout = file
		case strings.HasPrefix(args[at], "-"):
			inv.Close()
			return Invocation{}, refuse("%s is not an option a deploy saves with", args[at])
		default:
			inv.Argv = append(inv.Argv, args[at])
		}
	}
	return inv, nil
}

func (p Policy) openUnder(path string, flag int, perm fs.FileMode) (*os.File, error) {
	relative, err := filepath.Rel(p.StateRoot, path)
	if err != nil || !filepath.IsAbs(path) || relative == "." || strings.HasPrefix(relative, "..") {
		return nil, refuse("%s is not a file under %s", path, p.StateRoot)
	}
	root, err := os.OpenRoot(p.StateRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(relative, flag, perm)
	if err != nil {
		return nil, refuse("%s cannot be opened inside %s: %v", path, p.StateRoot, err)
	}
	return file, nil
}

func setOf(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}
