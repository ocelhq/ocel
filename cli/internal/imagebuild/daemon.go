package imagebuild

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/util/grpcutil/encoding/proto"
)

const (
	DockerHostEnv      = "DOCKER_HOST"
	DockerTLSVerifyEnv = "DOCKER_TLS_VERIFY"
	DockerCertPathEnv  = "DOCKER_CERT_PATH"
)

const (
	buildPath   = "/grpc"
	sessionPath = "/session"
	upgradeTo   = "h2c"

	snapshotterLabel = "org.mobyproject.buildkit.worker.snapshotter"

	handshakeTimeout = 10 * time.Second
)

type daemon struct {
	address string
	network string
	target  string
}

func openDaemon() (daemon, error) {
	host := os.Getenv(DockerHostEnv)
	if host == "" {
		host = platformAddress()
	}
	scheme, rest, split := strings.Cut(host, "://")
	if !split {
		return daemon{}, fmt.Errorf("%s is %q, which names no scheme: point it at a docker daemon as unix:///path/to/docker.sock or tcp://host:port", DockerHostEnv, host)
	}
	switch scheme {
	case "unix":
		return daemon{address: host, network: "unix", target: rest}, nil
	case "tcp", "http":
		if stated := statedTLS(); stated != "" {
			return daemon{}, fmt.Errorf("%s asks for a tls connection to the daemon at %s, and ocel speaks none: it would send the whole build context — your source tree — over plain tcp, where it can be read and the image it builds substituted: unset %s to accept that, or run the build on the machine the daemon is on", stated, host, stated)
		}
		return daemon{address: host, network: "tcp", target: strings.TrimSuffix(rest, "/")}, nil
	}
	return daemon{}, fmt.Errorf("%s is %q, and ocel builds images over unix:// and tcp:// only: set %s to one of those, or run the build where the daemon is", DockerHostEnv, host, DockerHostEnv)
}

func statedTLS() string {
	for _, name := range []string{DockerTLSVerifyEnv, DockerCertPathEnv} {
		if os.Getenv(name) != "" {
			return name
		}
	}
	return ""
}

func platformAddress() string {
	if runtime.GOOS == "windows" {
		return "npipe:////./pipe/docker_engine"
	}
	return "unix:///var/run/docker.sock"
}

func (d daemon) hijack(ctx context.Context, path, proto string, meta map[string][]string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, d.network, d.target)
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
		return nil, closing(conn, fmt.Errorf("the daemon at %s answered %q to the %s upgrade on %s, so it serves no builder", d.address, resp.Status, proto, path))
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, closing(conn, err)
	}
	if reader.Buffered() == 0 {
		return conn, nil
	}
	return &hijacked{Conn: conn, reader: io.MultiReader(io.LimitReader(reader, int64(reader.Buffered())), conn)}, nil
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
			return d.hijack(ctx, buildPath, upgradeTo, nil)
		}),
		client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return d.hijack(ctx, sessionPath, proto, meta)
		}),
	)
}

func (d daemon) mergeable(ctx context.Context, builder *client.Client) error {
	workers, err := builder.ListWorkers(ctx)
	if err != nil {
		return d.unreachable(err)
	}
	for _, worker := range workers {
		if worker.Labels[snapshotterLabel] != "" {
			return nil
		}
	}
	return fmt.Errorf("the docker daemon at %s keeps images in its classic store, where buildkit refuses the merge operations every railpack build is made of: turn the containerd image store on and restart docker (Docker Desktop: Settings → General → Use containerd; docker engine: \"features\": {\"containerd-snapshotter\": true} in /etc/docker/daemon.json), or set %s to a daemon that already has it", d.address, DockerHostEnv)
}

func (d daemon) tag(ctx context.Context, image Image) error {
	endpoint := "http://docker/images/" + url.PathEscape(image.Digest) + "/tag?" + url.Values{
		"repo": {image.Repository},
		"tag":  {image.Tag},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return fmt.Errorf("name %s in the daemon at %s: %w", image.Ref, d.address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the daemon at %s answered %q naming %s, so the image it just built cannot be reached by the ref a release pins: %s", d.address, resp.Status, image.Ref, strings.TrimSpace(string(said)))
	}
	return nil
}

func (d daemon) client() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, d.network, d.target)
		},
	}}
}

func (d daemon) unreachable(err error) error {
	return fmt.Errorf("no docker daemon answers at %s, and a container app's image is built by the one on this machine: start docker, or set %s to a daemon that is running\n    %v", d.address, DockerHostEnv, err)
}

func Reachable(ctx context.Context) error {
	d, err := openDaemon()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	conn, err := d.hijack(ctx, buildPath, upgradeTo, nil)
	if err != nil {
		return d.unreachable(err)
	}
	return conn.Close()
}
