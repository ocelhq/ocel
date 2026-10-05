package docker

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ocelhq/ocel/pkg/images"
)

const (
	pingTimeout = 5 * time.Second
	stopGrace   = 3 * time.Second

	StopsWithin = stopGrace + 3*time.Second

	concurrentStartWithin = 10 * time.Second

	exitLogLines = 20
)

type Spec struct {
	Name       string
	Image      string
	Args       []string
	User       string
	Env        []string
	Port       int
	Volume     string
	VolumePath string
	Labels     map[string]string
}

type Container struct {
	ID      string
	Address string
}

type Engine interface {
	Run(ctx context.Context, spec Spec) (Container, error)
	ReadExit(ctx context.Context, id string) (*Exited, error)
	Exec(ctx context.Context, id string, argv ...string) (string, error)
	ExecInput(ctx context.Context, id, input string, argv ...string) (string, error)
	Stop(ctx context.Context, id string) error
	Wipe(ctx context.Context, labels map[string]string) error
	Close() error
}

type OpenFunc func(ctx context.Context) (Engine, error)

const (
	LabelProject = "dev.ocel.project"
	LabelBackend = "dev.ocel.backend"
)

func Labels(project, backend string) map[string]string {
	return map[string]string{LabelProject: project, LabelBackend: backend}
}

func ProjectLabels(project string) map[string]string {
	return map[string]string{LabelProject: project}
}

func Name(project string, parts ...string) string {
	return strings.Join(append([]string{"ocel-dev", project}, parts...), "-")
}

type Unreachable struct {
	Address string
	For     string
	Err     error
}

func (u *Unreachable) Error() string {
	needed := u.For
	if needed == "" {
		needed = "a declared resource"
	}
	return fmt.Sprintf("no docker daemon answers at %s, and %s runs in a container on this machine: start docker, or set %s to a daemon that is running\n    %v", u.Address, needed, images.DockerHostEnv, u.Err)
}

func (u *Unreachable) Unwrap() error { return u.Err }

type ExecFailed struct {
	Argv   []string
	Code   int
	Output string
}

func (e *ExecFailed) Error() string {
	return fmt.Sprintf("%s exited %d: %s", e.Argv[0], e.Code, e.Output)
}

type Exited struct {
	Container string
	Code      int
	OOMKilled bool
	Removed   bool
	Logs      string
}

func (e *Exited) Error() string {
	if e.Removed {
		return fmt.Sprintf("the container %s was removed before it was ready", e.Container)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the container %s exited %d before it was ready", e.Container, e.Code)
	if e.OOMKilled {
		b.WriteString(", killed for running out of memory")
	}
	logs := strings.TrimRight(e.Logs, "\n")
	if logs == "" {
		b.WriteString(", and printed nothing")
		return b.String()
	}
	b.WriteString(", and the last lines it printed were:")
	for line := range strings.SplitSeq(logs, "\n") {
		b.WriteString("\n    " + line)
	}
	return b.String()
}

type daemon struct {
	api *client.Client
}

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

func Open(ctx context.Context) (Engine, error) {
	host, err := images.DockerHostFromEnv()
	if err != nil {
		return nil, &Unreachable{Address: cmp.Or(os.Getenv(images.DockerHostEnv), "its default address"), Err: err}
	}
	if err := refuseRemoteDaemon(host); err != nil {
		return nil, err
	}
	api, err := client.New(
		client.WithHost("tcp://docker"),
		client.WithDialContext(func(ctx context.Context, _, _ string) (net.Conn, error) { return host.Dial(ctx) }),
	)
	if err != nil {
		return nil, &Unreachable{Address: host.Address, Err: err}
	}
	pinging, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if _, err := api.Ping(pinging, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		_ = api.Close()
		return nil, &Unreachable{Address: host.Address, Err: err}
	}
	return &daemon{api: api}, nil
}

func refuseRemoteDaemon(host images.DockerHost) error {
	if host.Network != "tcp" {
		return nil
	}
	name, _, err := net.SplitHostPort(host.Target)
	if err != nil {
		name = host.Target
	}
	if ip, err := netip.ParseAddr(name); name == "localhost" || (err == nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%s is %s, a docker daemon on another machine: dev resources take no credentials, so running them there would publish them on every interface of %s: point %s at a docker daemon on this machine, or unset it", images.DockerHostEnv, host.Address, name, images.DockerHostEnv)
}

func (d *daemon) Close() error { return d.api.Close() }

func (d *daemon) Run(ctx context.Context, spec Spec) (Container, error) {
	if err := d.pull(ctx, spec.Image); err != nil {
		return Container{}, err
	}
	port, err := network.ParsePort(strconv.Itoa(spec.Port) + "/tcp")
	if err != nil {
		return Container{}, err
	}

	existing, err := d.api.ContainerInspect(ctx, spec.Name, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
	case err != nil:
		return Container{}, fmt.Errorf("look for the container %s: %w", spec.Name, err)
	case existing.Container.State != nil && existing.Container.State.Running && !isRunWithOtherArgs(existing.Container, spec):
		if !isFromSpec(existing.Container, spec) {
			return Container{}, fmt.Errorf("a container named %s is running and is not the %s this project asks for, so it may be in use: stop what started it, or remove it with `docker rm -f %s`", spec.Name, spec.Image, spec.Name)
		}
		return readPublishedAddress(existing.Container, spec.Name, port)
	default:
		if _, err := d.api.ContainerRemove(ctx, existing.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return Container{}, fmt.Errorf("replace the stopped container %s left by an earlier run: %w", spec.Name, err)
		}
	}

	hostConfig := &container.HostConfig{
		PortBindings: network.PortMap{port: {{HostIP: loopback}}},
	}
	if spec.Volume != "" {
		hostConfig.Mounts = []mount.Mount{{
			Type:          mount.TypeVolume,
			Source:        spec.Volume,
			Target:        spec.VolumePath,
			VolumeOptions: &mount.VolumeOptions{Labels: spec.Labels},
		}}
	}
	created, err := d.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: spec.Name,
		Config: &container.Config{
			Image:        spec.Image,
			Cmd:          spec.Args,
			User:         spec.User,
			Env:          spec.Env,
			Labels:       spec.Labels,
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: hostConfig,
	})
	if cerrdefs.IsConflict(err) {
		return d.waitForConcurrentStart(ctx, spec, port)
	}
	if err != nil {
		return Container{}, fmt.Errorf("create the container %s: %w", spec.Name, err)
	}
	if _, err := d.api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		d.discard(ctx, created.ID)
		return Container{}, fmt.Errorf("start the container %s: %w", spec.Name, err)
	}
	inspected, err := d.api.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		d.discard(ctx, created.ID)
		return Container{}, fmt.Errorf("read the port docker published for %s: %w", spec.Name, err)
	}
	running, err := readPublishedAddress(inspected.Container, spec.Name, port)
	if err != nil {
		d.discard(ctx, created.ID)
	}
	return running, err
}

func (d *daemon) waitForConcurrentStart(ctx context.Context, spec Spec, port network.Port) (Container, error) {
	var started container.InspectResponse
	err := poll(ctx, concurrentStartWithin, func(ctx context.Context) error {
		inspected, err := d.api.ContainerInspect(ctx, spec.Name, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if inspected.Container.State == nil || !inspected.Container.State.Running || !isFromSpec(inspected.Container, spec) {
			return fmt.Errorf("the container %s another process created is not running %s", spec.Name, spec.Image)
		}
		started = inspected.Container
		return nil
	})
	if err != nil {
		return Container{}, err
	}
	return readPublishedAddress(started, spec.Name, port)
}

func isFromSpec(inspected container.InspectResponse, spec Spec) bool {
	if inspected.Config == nil || inspected.Config.Image != spec.Image {
		return false
	}
	for key, value := range spec.Labels {
		if inspected.Config.Labels[key] != value {
			return false
		}
	}
	return true
}

func isRunWithOtherArgs(inspected container.InspectResponse, spec Spec) bool {
	return len(spec.Args) > 0 && isFromSpec(inspected, spec) && !slices.Equal(inspected.Config.Cmd, spec.Args)
}

func readPublishedAddress(inspected container.InspectResponse, name string, port network.Port) (Container, error) {
	var bindings []network.PortBinding
	if settings := inspected.NetworkSettings; settings != nil {
		bindings = settings.Ports[port]
	}
	if len(bindings) == 0 {
		return Container{}, fmt.Errorf("docker published no host port for %s", name)
	}
	return Container{ID: inspected.ID, Address: net.JoinHostPort(loopback.String(), bindings[0].HostPort)}, nil
}

func (d *daemon) discard(ctx context.Context, id string) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), StopsWithin)
	defer cancel()
	_ = d.Stop(detached, id)
}

func (d *daemon) pull(ctx context.Context, image string) error {
	if _, err := d.api.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("look for the image %s: %w", image, err)
	}
	pulling, err := d.api.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	defer func() { _ = pulling.Close() }()
	if err := pulling.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

func (d *daemon) ReadExit(ctx context.Context, id string) (*Exited, error) {
	inspected, err := d.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return &Exited{Container: id, Removed: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read whether the container %s is running: %w", id, err)
	}
	state := inspected.Container.State
	if state == nil || state.Running || state.Restarting || state.Status == container.StateCreated {
		return nil, nil
	}
	exited := &Exited{
		Container: cmp.Or(strings.TrimPrefix(inspected.Container.Name, "/"), id),
		Code:      state.ExitCode,
		OOMKilled: state.OOMKilled,
	}
	exited.Logs, err = d.readLastLines(ctx, id)
	if err != nil {
		exited.Logs = err.Error()
	}
	return exited, nil
}

func (d *daemon) readLastLines(ctx context.Context, id string) (string, error) {
	logs, err := d.api.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(exitLogLines)})
	if err != nil {
		return "", fmt.Errorf("read what the container printed: %w", err)
	}
	defer func() { _ = logs.Close() }()
	var printed bytes.Buffer
	if _, err := stdcopy.StdCopy(&printed, &printed, logs); err != nil {
		return "", fmt.Errorf("read what the container printed: %w", err)
	}
	return printed.String(), nil
}

func (d *daemon) Exec(ctx context.Context, id string, argv ...string) (string, error) {
	return d.ExecInput(ctx, id, "", argv...)
}

func (d *daemon) ExecInput(ctx context.Context, id, input string, argv ...string) (string, error) {
	created, err := d.api.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: argv, AttachStdin: input != "", AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", fmt.Errorf("run %s in the container: %w", argv[0], err)
	}
	attached, err := d.api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("run %s in the container: %w", argv[0], err)
	}
	defer attached.Close()
	if input != "" {
		if _, err := io.WriteString(attached.Conn, input); err != nil {
			return "", fmt.Errorf("write what %s reads: %w", argv[0], err)
		}
		if err := attached.CloseWrite(); err != nil {
			return "", fmt.Errorf("write what %s reads: %w", argv[0], err)
		}
	}

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return "", fmt.Errorf("read what %s printed: %w", argv[0], err)
	}
	inspected, err := d.api.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("read how %s exited: %w", argv[0], err)
	}
	if inspected.ExitCode != 0 {
		return "", &ExecFailed{Argv: argv, Code: inspected.ExitCode, Output: stdout.String() + stderr.String()}
	}
	return stdout.String(), nil
}

func (d *daemon) Stop(ctx context.Context, id string) error {
	timeout := int(stopGrace / time.Second)
	if _, err := d.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("stop the container %s: %w", id, err)
	}
	if _, err := d.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove the container %s: %w", id, err)
	}
	return nil
}

func (d *daemon) Wipe(ctx context.Context, labels map[string]string) error {
	filters := make(client.Filters)
	for key, value := range labels {
		filters.Add("label", key+"="+value)
	}
	containers, err := d.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return fmt.Errorf("list this project's containers: %w", err)
	}
	var failed []error
	for _, summary := range containers.Items {
		failed = append(failed, d.Stop(ctx, summary.ID))
	}
	volumes, err := d.api.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return errors.Join(append(failed, fmt.Errorf("list this project's volumes: %w", err))...)
	}
	for _, vol := range volumes.Items {
		if _, err := d.api.VolumeRemove(ctx, vol.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			failed = append(failed, fmt.Errorf("remove the volume %s: %w", vol.Name, err))
		}
	}
	return errors.Join(failed...)
}
