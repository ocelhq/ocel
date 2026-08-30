package imagebuild

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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

	handshakeTimeout = 10 * time.Second
)

type daemon struct {
	providerkit.DockerHost
}

func openDaemon() (daemon, error) {
	host, err := providerkit.OpenDockerHost()
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

func (d daemon) usable(ctx context.Context, builder *client.Client) error {
	workers, err := builder.ListWorkers(ctx)
	if err != nil {
		return d.noBuilder(err)
	}
	return d.addressable(workers)
}

func (d daemon) addressable(workers []*client.WorkerInfo) error {
	for _, worker := range workers {
		if worker.Labels[snapshotterLabel] != "" {
			return nil
		}
	}
	return fmt.Errorf("the docker daemon at %s keeps images in its classic store, where an image is not addressable by the digest it is built under, and where buildkit additionally refuses the merge operations a railpack plan is assembled from: turn the containerd image store on and restart docker (Docker Desktop: Settings → General → Use containerd; docker engine: \"features\": {\"containerd-snapshotter\": true} in /etc/docker/daemon.json), or set %s to a daemon that already has it", d.Address, providerkit.DockerHostEnv)
}

func (d daemon) tag(ctx context.Context, image Image) error {
	transport := d.Transport()
	defer transport.CloseIdleConnections()
	return d.Tag(ctx, &http.Client{Transport: transport}, image.Digest, image.Repository, image.Tag)
}

func (d daemon) unreachable(err error) error {
	return fmt.Errorf("no docker daemon answers at %s, and a container app's image is built by the one on this machine: start docker, or set %s to a daemon that is running\n    %v", d.Address, providerkit.DockerHostEnv, err)
}

func (d daemon) noBuilder(err error) error {
	return fmt.Errorf("the daemon at %s never named a builder to run the build on: start docker, or set %s to a daemon that is running\n    %v", d.Address, providerkit.DockerHostEnv, err)
}

func Reachable(ctx context.Context) error {
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
	return d.usable(ctx, builder)
}
