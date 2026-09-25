package imagebuild

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/util/grpcutil/encoding/proto"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	buildPath   = "/grpc"
	sessionPath = "/session"
	upgradeTo   = "h2c"

	snapshotterLabel = "org.mobyproject.buildkit.worker.snapshotter"
	containerOS      = "linux"

	handshakeTimeout = 10 * time.Second
)

type daemon struct {
	providerkit.DockerHost
}

func openDaemon() (daemon, error) {
	host, err := providerkit.DockerHostFromEnv()
	if err != nil {
		return daemon{}, err
	}
	return daemon{DockerHost: host}, nil
}

func (d daemon) hijack(ctx context.Context, path, proto string, meta map[string][]string) (net.Conn, error) {
	conn, err := d.Dial(ctx)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, closing(conn, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+path, nil)
	if err != nil {
		return nil, closing(conn, err)
	}
	req.Host = "docker"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", proto)
	for name, values := range meta {
		req.Header[http.CanonicalHeaderKey(name)] = values
	}
	if err := req.Write(conn); err != nil {
		return nil, closing(conn, err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, closing(conn, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, closing(conn, fmt.Errorf("the daemon at %s answered %q to the %s upgrade on %s, so it serves no builder", d.Address, resp.Status, proto, path))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, closing(conn, err)
	}
	if reader.Buffered() == 0 {
		return conn, nil
	}
	return &hijacked{Conn: conn, reader: io.MultiReader(io.LimitReader(reader, int64(reader.Buffered())), conn)}, nil
}

func (d daemon) handshake(ctx context.Context, path, proto string, meta map[string][]string) (net.Conn, error) {
	if _, set := ctx.Deadline(); !set {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, handshakeTimeout)
		defer cancel()
	}
	return d.hijack(ctx, path, proto, meta)
}

func closing(conn net.Conn, err error) error {
	_ = conn.Close()
	return err
}

type hijacked struct {
	net.Conn
	reader io.Reader
}

func (h *hijacked) Read(p []byte) (int, error) { return h.reader.Read(p) }

func (d daemon) builder(ctx context.Context) (*client.Client, error) {
	return client.New(ctx, "",
		client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return d.handshake(ctx, buildPath, upgradeTo, nil)
		}),
		client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return d.handshake(ctx, sessionPath, proto, meta)
		}),
	)
}

func (d daemon) usable(ctx context.Context, builder *client.Client, arches ...string) error {
	workers, err := builder.ListWorkers(ctx)
	if err != nil {
		return d.noBuilder(err)
	}
	exporting, err := d.addressable(workers)
	if err != nil {
		return err
	}
	for _, arch := range arches {
		if arch == "" {
			continue
		}
		if err := d.buildsFor(exporting, arch); err != nil {
			return err
		}
	}
	return nil
}

func (d daemon) buildsFor(workers []*client.WorkerInfo, arch string) error {
	var runs []string
	for _, worker := range workers {
		for _, platform := range worker.Platforms {
			if platform.OS == containerOS && platform.Architecture == arch {
				return nil
			}
			runs = append(runs, platform.OS+"/"+platform.Architecture)
		}
	}
	slices.Sort(runs)
	target := providerkit.ContainerPlatform(arch)
	return fmt.Errorf("the target runs %s, and the docker daemon at %s builds for %s alone: give it an emulator for %s (docker run --privileged --rm tonistiigi/binfmt --install %s, then restart docker), or set %s to a daemon running on a %s machine",
		target, d.Address, builtFor(runs), target, arch, providerkit.DockerHostEnv, target)
}

func builtFor(runs []string) string {
	if len(runs) == 0 {
		return "no platform it names"
	}
	return strings.Join(slices.Compact(runs), ", ")
}

func (d daemon) addressable(workers []*client.WorkerInfo) ([]*client.WorkerInfo, error) {
	var exporting []*client.WorkerInfo
	for _, worker := range workers {
		if worker.Labels[snapshotterLabel] != "" {
			exporting = append(exporting, worker)
		}
	}
	if len(exporting) > 0 {
		return exporting, nil
	}
	return nil, fmt.Errorf("the docker daemon at %s keeps images in its classic store, where an image is not addressable by the digest it is built under, and where buildkit additionally refuses the merge operations a railpack plan is assembled from: turn the containerd image store on and restart docker (Docker Desktop: Settings → General → Use containerd; docker engine: \"features\": {\"containerd-snapshotter\": true} in /etc/docker/daemon.json), or set %s to a daemon that already has it", d.Address, providerkit.DockerHostEnv)
}

func (d daemon) tag(ctx context.Context, image Image) error {
	transport := d.Transport()
	defer transport.CloseIdleConnections()
	return d.Tag(ctx, &http.Client{Transport: transport}, image.Digest, image.Repository, image.Tag)
}

func (d daemon) unreachable(err error) error {
	return fmt.Errorf("no docker daemon answers at %s, and a container app's image is built by the one on this machine: start docker, or set %s to a daemon that is running\n    %w", d.Address, providerkit.DockerHostEnv, err)
}

func (d daemon) noBuilder(err error) error {
	return fmt.Errorf("the daemon at %s never named a builder to run the build on: start docker, or set %s to a daemon that is running\n    %w", d.Address, providerkit.DockerHostEnv, err)
}

func Reachable(ctx context.Context, arches ...string) error {
	d, err := openDaemon()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	builder, err := d.builder(ctx)
	if err != nil {
		return d.unreachable(err)
	}
	defer func() { _ = builder.Close() }()
	return d.usable(ctx, builder, arches...)
}
