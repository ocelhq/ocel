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
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	pingTimeout = 5 * time.Second
	stopGrace   = 3 * time.Second

	StopsWithin = stopGrace + 3*time.Second

	winnerStartsWithin = 10 * time.Second
)

type Spec struct {
	Name       string
	Image      string
	Env        []string
	Port       int
	Volume     string
	VolumePath string
	Labels     map[string]string
}

type Container struct {
	ID   string
	Addr string
}

type Engine interface {
	Run(ctx context.Context, spec Spec) (Container, error)
	Exec(ctx context.Context, id string, argv ...string) (string, error)
	ExecInput(ctx context.Context, id, input string, argv ...string) (string, error)
	Stop(ctx context.Context, id string) error
	Wipe(ctx context.Context, labels map[string]string) error
	Close() error
}

type Opener func(ctx context.Context) (Engine, error)

const (
	LabelProject   = "dev.ocel.project"
	LabelComponent = "dev.ocel.component"
)

func Labels(project, component string) map[string]string {
	return map[string]string{LabelProject: project, LabelComponent: component}
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
	return fmt.Sprintf("no docker daemon answers at %s, and %s runs in a container on this machine: start docker, or set %s to a daemon that is running\n    %v", u.Address, needed, providerkit.DockerHostEnv, u.Err)
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

type daemon struct {
	api       *client.Client
	publishOn netip.Addr
	reachedAt string
}

func Open(ctx context.Context) (Engine, error) {
	host, err := providerkit.DockerHostFromEnv()
	if err != nil {
		return nil, &Unreachable{Address: cmp.Or(os.Getenv(providerkit.DockerHostEnv), "its default address"), Err: err}
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
	publishOn, reachedAt := published(host)
	return &daemon{api: api, publishOn: publishOn, reachedAt: reachedAt}, nil
}

func published(host providerkit.DockerHost) (netip.Addr, string) {
	loopback := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if host.Network != "tcp" {
		return loopback, loopback.String()
	}
	name, _, err := net.SplitHostPort(host.Target)
	if err != nil {
		name = host.Target
	}
	if ip, err := netip.ParseAddr(name); name == "localhost" || (err == nil && ip.IsLoopback()) {
		return loopback, loopback.String()
	}
	return netip.IPv4Unspecified(), name
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

	held, err := d.api.ContainerInspect(ctx, spec.Name, client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
	case err != nil:
		return Container{}, fmt.Errorf("look for the container %s: %w", spec.Name, err)
	case held.Container.State != nil && held.Container.State.Running:
		if !runs(held.Container, spec) {
			return Container{}, fmt.Errorf("a container named %s is running and is not the %s this project asks for, so it may be in use: stop what started it, or remove it with `docker rm -f %s`", spec.Name, spec.Image, spec.Name)
		}
		return d.reachable(held.Container, spec.Name, port)
	default:
		if _, err := d.api.ContainerRemove(ctx, held.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return Container{}, fmt.Errorf("replace the stopped container %s left by an earlier run: %w", spec.Name, err)
		}
	}

	hostConfig := &container.HostConfig{
		PortBindings: network.PortMap{port: {{HostIP: d.publishOn}}},
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
			Env:          spec.Env,
			Labels:       spec.Labels,
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: hostConfig,
	})
	if cerrdefs.IsConflict(err) {
		return d.adoptWinner(ctx, spec, port)
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
	running, err := d.reachable(inspected.Container, spec.Name, port)
	if err != nil {
		d.discard(ctx, created.ID)
	}
	return running, err
}

func (d *daemon) adoptWinner(ctx context.Context, spec Spec, port network.Port) (Container, error) {
	var winner container.InspectResponse
	err := WaitReady(ctx, winnerStartsWithin, func(ctx context.Context) error {
		held, err := d.api.ContainerInspect(ctx, spec.Name, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if held.Container.State == nil || !held.Container.State.Running || !runs(held.Container, spec) {
			return fmt.Errorf("the container %s another process created is not running %s", spec.Name, spec.Image)
		}
		winner = held.Container
		return nil
	})
	if err != nil {
		return Container{}, err
	}
	return d.reachable(winner, spec.Name, port)
}

func runs(held container.InspectResponse, spec Spec) bool {
	if held.Config == nil || held.Config.Image != spec.Image {
		return false
	}
	for key, value := range spec.Labels {
		if held.Config.Labels[key] != value {
			return false
		}
	}
	return true
}

func (d *daemon) reachable(held container.InspectResponse, name string, port network.Port) (Container, error) {
	var bindings []network.PortBinding
	if settings := held.NetworkSettings; settings != nil {
		bindings = settings.Ports[port]
	}
	if len(bindings) == 0 {
		return Container{}, fmt.Errorf("docker published no host port for %s", name)
	}
	return Container{ID: held.ID, Addr: net.JoinHostPort(d.reachedAt, bindings[0].HostPort)}, nil
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
	for _, held := range containers.Items {
		failed = append(failed, d.Stop(ctx, held.ID))
	}
	volumes, err := d.api.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return errors.Join(append(failed, fmt.Errorf("list this project's volumes: %w", err))...)
	}
	for _, held := range volumes.Items {
		if _, err := d.api.VolumeRemove(ctx, held.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			failed = append(failed, fmt.Errorf("remove the volume %s: %w", held.Name, err))
		}
	}
	return errors.Join(failed...)
}
