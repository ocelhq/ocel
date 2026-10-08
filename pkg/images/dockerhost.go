package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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

func DockerHostFromEnv() (DockerHost, error) {
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

func (d DockerHost) Export(ctx context.Context, client *http.Client, ref string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/images/"+ref+"/get", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s out of the daemon at %s: %w", ref, d.Address, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, fmt.Errorf("the daemon at %s answered %q reading %s out as a tar stream: %s",
			d.Address, resp.Status, ref, readErrorBody(resp.Body))
	}
	return resp.Body, nil
}

type ImageInspection struct {
	Architecture  string
	ContentDigest string
}

type imageDescription struct {
	Architecture string         `json:"Architecture"`
	Variant      string         `json:"Variant"`
	OS           string         `json:"Os"`
	OSVersion    string         `json:"OsVersion"`
	Config       map[string]any `json:"Config"`
	RootFS       struct {
		Layers []string `json:"Layers"`
	} `json:"RootFS"`
}

func (d DockerHost) Inspect(ctx context.Context, client *http.Client, ref string) (ImageInspection, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/images/"+ref+"/json", nil)
	if err != nil {
		return ImageInspection{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ImageInspection{}, fmt.Errorf("inspect %s in the daemon at %s: %w", ref, d.Address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ImageInspection{}, fmt.Errorf("the daemon at %s answered %q inspecting %s: %s", d.Address, resp.Status, ref, readErrorBody(resp.Body))
	}
	var described imageDescription
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&described); err != nil {
		return ImageInspection{}, fmt.Errorf("read what the daemon at %s said about %s: %w", d.Address, ref, err)
	}
	if described.Architecture == "" {
		return ImageInspection{}, fmt.Errorf("the daemon at %s names no architecture for %s", d.Address, ref)
	}
	digest, err := contentDigest(described)
	if err != nil {
		return ImageInspection{}, fmt.Errorf("digest the content of %s as the daemon at %s describes it: %w", ref, d.Address, err)
	}
	return ImageInspection{Architecture: described.Architecture, ContentDigest: digest}, nil
}

func contentDigest(described imageDescription) (string, error) {
	config := maps.Clone(described.Config)
	delete(config, "Image")
	encoded, err := json.Marshal(struct {
		Architecture string         `json:"architecture"`
		Variant      string         `json:"variant,omitempty"`
		OS           string         `json:"os"`
		OSVersion    string         `json:"os_version,omitempty"`
		Config       map[string]any `json:"config"`
		Layers       []string       `json:"layers"`
	}{described.Architecture, described.Variant, described.OS, described.OSVersion, config, described.RootFS.Layers})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
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
			d.Address, resp.Status, source, repository, tag, readErrorBody(resp.Body))
	}
	return nil
}
