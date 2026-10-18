package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/vps/box/recordstore"
	"github.com/ocelhq/ocel/platform/vps/box/sealkey"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	ProcRoot      = "/proc"
	headerWindow  = 5 * time.Second
	answerWindow  = 30 * time.Second
	inspectWindow = 10 * time.Second
	inspectLimit  = 4 << 20
)

var containerScope = regexp.MustCompile(`docker[-/]([0-9a-f]{64})(?:\.scope)?$`)

type peerKey struct{}

type peer struct {
	pid int
	err error
}

func PeerPID(conn net.Conn) (int, error) {
	over, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("the connection came over %s, not a unix socket", conn.RemoteAddr().Network())
	}
	raw, err := over.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var asked error
	if err := raw.Control(func(fd uintptr) {
		cred, asked = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if asked != nil {
		return 0, fmt.Errorf("read the caller's credentials: %w", asked)
	}
	return int(cred.Pid), nil
}

func ContainerID(cgroup string) (string, bool) {
	for line := range strings.SplitSeq(cgroup, "\n") {
		_, path, split := strings.Cut(line, "::")
		if !split {
			fields := strings.SplitN(line, ":", 3)
			if len(fields) != 3 {
				continue
			}
			path = fields[2]
		}
		if match := containerScope.FindStringSubmatch(strings.TrimSpace(path)); match != nil {
			return match[1], true
		}
	}
	return "", false
}

type Container struct {
	Manifest string
	InitPID  int
}

type Inspector interface {
	ReadContainer(ctx context.Context, container string) (Container, error)
}

type Resolver interface {
	Resolve(ctx context.Context, manifest boxapi.Manifest) (map[string]string, error)
}

type Measurer interface {
	Space(ctx context.Context, volume string) (free uint64, total uint64, err error)
}

type Server struct {
	Proc    string
	Inspect Inspector
	Resolve Resolver
	Space   Measurer
	Queues  Queues
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: headerWindow,
		WriteTimeout:      answerWindow,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			pid, err := PeerPID(conn)
			return context.WithValue(ctx, peerKey{}, peer{pid: pid, err: err})
		},
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.Background(), headerWindow)
		defer cancel()
		_ = server.Shutdown(drain)
	}()
	err := server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return err
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodGet+" "+boxapi.ValuesPath, s.answer)
	mux.HandleFunc(http.MethodGet+" "+boxapi.SpacePath, s.measure)
	if s.Queues != nil {
		s.mountQueues(mux)
	}
	return mux
}

func (s *Server) manifestOf(w http.ResponseWriter, r *http.Request) (boxapi.Manifest, bool) {
	manifest, refused := s.callerManifest(r.Context())
	if refused != nil {
		http.Error(w, refused.Error(), refused.status)
		return boxapi.Manifest{}, false
	}
	return manifest, true
}

type callerRefusal struct {
	status int
	reason string
}

func (c *callerRefusal) Error() string { return c.reason }

func (s *Server) callerManifest(ctx context.Context) (boxapi.Manifest, *callerRefusal) {
	caller, _ := ctx.Value(peerKey{}).(peer)
	if caller.err != nil {
		return boxapi.Manifest{}, &callerRefusal{http.StatusForbidden, "the caller could not be identified: " + caller.err.Error()}
	}
	container, err := s.containerOf(caller.pid)
	if err != nil {
		return boxapi.Manifest{}, &callerRefusal{http.StatusForbidden, err.Error()}
	}
	inspecting, cancel := context.WithTimeout(ctx, inspectWindow)
	defer cancel()
	read, err := s.Inspect.ReadContainer(inspecting, container)
	if err != nil {
		return boxapi.Manifest{}, &callerRefusal{http.StatusBadGateway, "read the caller's container: " + err.Error()}
	}
	if read.InitPID <= 0 || read.InitPID != caller.pid {
		return boxapi.Manifest{}, &callerRefusal{http.StatusForbidden, fmt.Sprintf("the caller, pid %d, is not its container's init process (pid %d): only a container's init process is answered", caller.pid, read.InitPID)}
	}
	if read.Manifest == "" {
		return boxapi.Manifest{}, &callerRefusal{http.StatusNotFound, "the caller's container has no live-value manifest"}
	}
	manifest, err := boxapi.Parse([]byte(read.Manifest))
	if err != nil {
		return boxapi.Manifest{}, &callerRefusal{http.StatusBadGateway, err.Error()}
	}
	if !manifest.Live() {
		return boxapi.Manifest{}, &callerRefusal{http.StatusNotFound, "the caller's manifest names nothing live"}
	}
	return manifest, nil
}

func (s *Server) measure(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.manifestOf(w, r)
	if !ok {
		return
	}
	if manifest.Store == nil || manifest.Store.Volume == "" || s.Space == nil {
		http.Error(w, "the caller's container names no store volume this box has", http.StatusNotFound)
		return
	}
	free, total, err := s.Space.Space(r.Context(), manifest.Store.Volume)
	if err != nil {
		http.Error(w, "measure "+manifest.Store.Volume+": "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(boxapi.Space{Free: free, Total: total})
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	manifest, ok := s.manifestOf(w, r)
	if !ok {
		return
	}
	resolved, err := s.Resolve.Resolve(r.Context(), manifest)
	if err != nil {
		http.Error(w, "resolve the caller's values: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(boxapi.Answer{Values: resolved})
}

func (s *Server) containerOf(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("the caller presented no pid")
	}
	proc := s.Proc
	if proc == "" {
		proc = ProcRoot
	}
	cgroup, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", fmt.Errorf("the caller's cgroup could not be read: %w", err)
	}
	container, found := ContainerID(string(cgroup))
	if !found {
		return "", errors.New("the caller is not in a container on this box")
	}
	return container, nil
}

type Docker struct {
	host      images.DockerHost
	transport *http.Transport
}

func NewDocker() (*Docker, error) {
	host, err := images.DockerHostFromEnv()
	if err != nil {
		return nil, err
	}
	return &Docker{host: host, transport: host.Transport()}, nil
}

func (d *Docker) ReadContainer(ctx context.Context, container string) (Container, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+container+"/json", nil)
	if err != nil {
		return Container{}, err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return Container{}, fmt.Errorf("ask the daemon at %s about the container: %w", d.host.Address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Container{}, fmt.Errorf("the daemon at %s answered %q about the container: %s", d.host.Address, resp.Status, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		State struct {
			Pid int `json:"Pid"`
		} `json:"State"`
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return Container{}, fmt.Errorf("read what the daemon says about the container: %w", err)
	}
	return Container{Manifest: ManifestIn(inspected.Config.Env), InitPID: inspected.State.Pid}, nil
}

func (d *Docker) Address(ctx context.Context, network, container string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(container)+"/json", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("ask the daemon at %s about %s: %w", d.host.Address, container, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("the daemon at %s answered %q about %s: %s", d.host.Address, resp.Status, container, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return "", fmt.Errorf("read what the daemon says about %s: %w", container, err)
	}
	if !inspected.State.Running {
		return "", fmt.Errorf("%s is not running", container)
	}
	if address := inspected.NetworkSettings.Networks[network].IPAddress; address != "" {
		return address, nil
	}
	return "", fmt.Errorf("%s holds no address on %s", container, network)
}

func (d *Docker) IsOcels(ctx context.Context, container string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(container)+"/json", nil)
	if err != nil {
		return false, err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return false, fmt.Errorf("ask the daemon at %s about %s: %w", d.host.Address, container, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("the daemon at %s answered %q about %s: %s", d.host.Address, resp.Status, container, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return false, fmt.Errorf("read what the daemon says about %s: %w", container, err)
	}
	return inspected.Config.Labels[boxapi.TierLabel] != "", nil
}

func (d *Docker) IsHostBound(ctx context.Context, volume string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/volumes/"+url.PathEscape(volume), nil)
	if err != nil {
		return false, err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return false, fmt.Errorf("ask the daemon at %s about volume %s: %w", d.host.Address, volume, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("the daemon at %s answered %q about volume %s: %s", d.host.Address, resp.Status, volume, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		Options map[string]string `json:"Options"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return false, fmt.Errorf("read what the daemon says about volume %s: %w", volume, err)
	}
	binding := slices.ContainsFunc(strings.Split(inspected.Options["o"], ","), func(option string) bool { return option == "bind" || option == "rbind" })
	return binding || strings.HasPrefix(inspected.Options["device"], "/"), nil
}

func (d *Docker) MountSource(ctx context.Context, container, destination string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(container)+"/json", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("ask the daemon at %s about %s: %w", d.host.Address, container, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("the daemon at %s answered %q about %s: %s", d.host.Address, resp.Status, container, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return "", fmt.Errorf("read what the daemon says about %s: %w", container, err)
	}
	for _, mount := range inspected.Mounts {
		if mount.Destination == destination && mount.Source != "" {
			return mount.Source, nil
		}
	}
	return "", fmt.Errorf("%s mounts nothing at %s", container, destination)
}

func (d *Docker) Space(ctx context.Context, volume string) (uint64, uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/volumes/"+url.PathEscape(volume), nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ask the daemon at %s about volume %s: %w", d.host.Address, volume, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, 0, fmt.Errorf("the daemon at %s answered %q about volume %s: %s", d.host.Address, resp.Status, volume, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		Mountpoint string `json:"Mountpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return 0, 0, fmt.Errorf("read what the daemon says about volume %s: %w", volume, err)
	}
	if inspected.Mountpoint == "" {
		return 0, 0, fmt.Errorf("the daemon reports no mountpoint for volume %s", volume)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(inspected.Mountpoint, &stat); err != nil {
		return 0, 0, fmt.Errorf("measure %s: %w", inspected.Mountpoint, err)
	}
	return stat.Bavail * uint64(stat.Bsize), stat.Blocks * uint64(stat.Bsize), nil
}

func ManifestIn(env []string) string {
	for _, entry := range env {
		if value, named := strings.CutPrefix(entry, boxapi.EnvVar+"="); named {
			return value
		}
	}
	return ""
}

type Store struct {
	TierRoot     string
	StateRoot    string
	RoutingTable string
}

func (s Store) Resolve(ctx context.Context, manifest boxapi.Manifest) (map[string]string, error) {
	reader := variablestore.EnvironmentReader{
		KeyValues:   recordstore.KeyValues{Root: s.StateRoot},
		Cipher:      sealkey.Cipher{Root: s.TierRoot},
		Scope:       variablestore.Scope{Project: manifest.Slug, Tier: environment.Tier(manifest.Tier)},
		Environment: manifest.Environment,
	}
	cells := make([]variablestore.Cell, 0, len(manifest.Keys))
	for _, key := range manifest.Keys {
		cells = append(cells, variablestore.Cell{Folder: key.Folder, Key: key.Key})
	}
	answer, _, err := live.Fetch(ctx, reader, cells, manifest.Bindings)
	if err != nil {
		return nil, err
	}
	if manifest.Store == nil {
		return answer, nil
	}
	base, err := s.storeBase(manifest)
	if err != nil {
		return nil, err
	}
	if base != "" {
		answer[boxapi.StorePublicKey] = base
		published(answer, manifest.Bindings, base)
	}
	if manifest.Store.Sealed == "" {
		return answer, nil
	}
	secret, err := s.storeSecret(ctx, manifest)
	if err != nil {
		return nil, err
	}
	answer[boxapi.StoreSecretKey] = secret
	return answer, nil
}

func (s Store) storeBase(manifest boxapi.Manifest) (string, error) {
	if manifest.Store.Pointer == "" {
		return "", nil
	}
	at := s.RoutingTable
	if at == "" {
		at = boxapi.RoutingTablePath
	}
	table, err := os.ReadFile(at)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the routing table %s cannot be read: %w", at, err)
	}
	read, err := boxapi.ReadRoutingTable(table)
	if err != nil {
		return "", fmt.Errorf("the routing table %s is refused: %w", at, err)
	}
	return boxapi.StoreBase(read.Claims, boxapi.Surface(manifest.Slug, manifest.Tier), manifest.Store.Pointer), nil
}

func (s Store) storeSecret(ctx context.Context, manifest boxapi.Manifest) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(manifest.Store.Sealed)
	if err != nil {
		return "", fmt.Errorf("the manifest's %s is not valid base64", boxapi.StoreSecretName)
	}
	bound, err := boxapi.NewStoreSecretAssociatedData(manifest.Slug, environment.Tier(manifest.Tier), manifest.Store.Env)
	if err != nil {
		return "", err
	}
	opened, err := (sealkey.Cipher{Root: s.TierRoot}).Open(ctx, environment.Tier(manifest.Tier), bound, sealed)
	if err != nil {
		return "", err
	}
	return string(opened), nil
}

func published(answer map[string]string, bindings []live.Binding, base string) {
	for _, binding := range bindings {
		if binding.Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET {
			continue
		}
		encoded, ok := answer[binding.Key]
		if !ok {
			continue
		}
		record := &bindingsv1.Binding{}
		if err := protojson.Unmarshal([]byte(encoded), record); err != nil {
			continue
		}
		properties := record.GetBucket()
		if properties == nil || !properties.GetPublic() {
			continue
		}
		properties.PublicBaseUrl = base + "/" + properties.GetBucket()
		republished, err := protojson.Marshal(record)
		if err != nil {
			continue
		}
		answer[binding.Key] = string(republished)
	}
}
