package providerkit

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	DockerHostEnv      = "DOCKER_HOST"
	DockerTLSVerifyEnv = "DOCKER_TLS_VERIFY"
	DockerCertPathEnv  = "DOCKER_CERT_PATH"
)

const PipeNetwork = "npipe"

type DockerHost struct {
	Address string
	Network string
	Target  string
}

func OpenDockerHost() (DockerHost, error) {
	host := os.Getenv(DockerHostEnv)
	if host == "" {
		host = platformDockerAddress()
	}
	scheme, rest, split := strings.Cut(host, "://")
	if !split {
		return DockerHost{}, fmt.Errorf("%s is %q, which names no scheme: point it at a docker daemon as unix:///path/to/docker.sock or tcp://host:port", DockerHostEnv, host)
	}
	switch scheme {
	case "unix":
		return DockerHost{Address: host, Network: "unix", Target: rest}, nil
	case "tcp", "http":
		if stated := statedDockerTLS(); stated != "" {
			return DockerHost{}, fmt.Errorf("%s asks for a tls connection to the daemon at %s, and ocel speaks none: it would send the whole build context — your source tree — over plain tcp, where it can be read and the image it builds substituted: unset %s to accept that, or run the build on the machine the daemon is on", stated, host, stated)
		}
		return DockerHost{Address: host, Network: "tcp", Target: strings.TrimSuffix(rest, "/")}, nil
	case PipeNetwork:
		if d, ok := pipeDockerHost(host, rest); ok {
			return d, nil
		}
	}
	return DockerHost{}, fmt.Errorf("%s is %q, and ocel reaches a docker daemon over unix://, tcp://, and npipe:// on windows: set %s to one of those, or run where the daemon is", DockerHostEnv, host, DockerHostEnv)
}

func statedDockerTLS() string {
	for _, name := range []string{DockerTLSVerifyEnv, DockerCertPathEnv} {
		if os.Getenv(name) != "" {
			return name
		}
	}
	return ""
}

func (d DockerHost) Dial(ctx context.Context) (net.Conn, error) {
	return dialDocker(ctx, d.Network, d.Target)
}

func (d DockerHost) Transport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialDocker(ctx, d.Network, d.Target)
		},
	}
}

func (d DockerHost) Tag(ctx context.Context, client *http.Client, source, repository, tag string) error {
	endpoint := "http://docker/images/" + source + "/tag?" + url.Values{"repo": {repository}, "tag": {tag}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("name %s as %s:%s in the daemon at %s: %w", source, repository, tag, d.Address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("the daemon at %s answered %q naming %s as %s:%s, so the image cannot be reached by the coordinate it is released under: %s",
			d.Address, resp.Status, source, repository, tag, said(resp.Body))
	}
	return nil
}
