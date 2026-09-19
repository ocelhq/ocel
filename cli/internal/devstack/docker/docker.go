package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
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
	stopTimeout = 10
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
	Stop(ctx context.Context, id string) error
	RemoveVolumes(ctx context.Context, labels map[string]string) error
	Close() error
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
	api *client.Client
}

func Open(ctx context.Context) (Engine, error) {
	host, err := providerkit.OpenDockerHost()
	if err != nil {
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

func (d *daemon) Close() error { return d.api.Close() }

func (d *daemon) Run(ctx context.Context, spec Spec) (Container, error) {
	if err := d.pull(ctx, spec.Image); err != nil {
		return Container{}, err
	}
	if _, err := d.api.ContainerRemove(ctx, spec.Name, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return Container{}, fmt.Errorf("replace the container %s left by an earlier run: %w", spec.Name, err)
	}

	port, err := network.ParsePort(strconv.Itoa(spec.Port) + "/tcp")
	if err != nil {
		return Container{}, err
	}
	hostConfig := &container.HostConfig{
		PortBindings: network.PortMap{port: {{HostIP: netip.AddrFrom4([4]byte{127, 0, 0, 1})}}},
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
	if err != nil {
		return Container{}, fmt.Errorf("create the container %s: %w", spec.Name, err)
	}
	if _, err := d.api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_ = d.Stop(ctx, created.ID)
		return Container{}, fmt.Errorf("start the container %s: %w", spec.Name, err)
	}

	inspected, err := d.api.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		_ = d.Stop(ctx, created.ID)
		return Container{}, fmt.Errorf("read the port docker published for %s: %w", spec.Name, err)
	}
	var bindings []network.PortBinding
	if settings := inspected.Container.NetworkSettings; settings != nil {
		bindings = settings.Ports[port]
	}
	if len(bindings) == 0 {
		_ = d.Stop(ctx, created.ID)
		return Container{}, fmt.Errorf("docker published no host port for %s", spec.Name)
	}
	return Container{ID: created.ID, Addr: net.JoinHostPort("127.0.0.1", bindings[0].HostPort)}, nil
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
	created, err := d.api.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: argv, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", fmt.Errorf("run %s in the container: %w", argv[0], err)
	}
	attached, err := d.api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("run %s in the container: %w", argv[0], err)
	}
	defer attached.Close()

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
	timeout := stopTimeout
	if _, err := d.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("stop the container %s: %w", id, err)
	}
	if _, err := d.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove the container %s: %w", id, err)
	}
	return nil
}

func (d *daemon) RemoveVolumes(ctx context.Context, labels map[string]string) error {
	filters := make(client.Filters)
	for key, value := range labels {
		filters.Add("label", key+"="+value)
	}
	containers, err := d.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return fmt.Errorf("list this project's containers: %w", err)
	}
	for _, held := range containers.Items {
		if err := d.Stop(ctx, held.ID); err != nil {
			return err
		}
	}
	volumes, err := d.api.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("list this project's volumes: %w", err)
	}
	var failed []error
	for _, held := range volumes.Items {
		if _, err := d.api.VolumeRemove(ctx, held.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			failed = append(failed, fmt.Errorf("remove the volume %s: %w", held.Name, err))
		}
	}
	return errors.Join(failed...)
}
