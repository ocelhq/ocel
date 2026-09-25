package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
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

type Inspector interface {
	Manifest(ctx context.Context, container string) (string, error)
}

type Resolver interface {
	Resolve(ctx context.Context, manifest vars.Manifest) (map[string]string, error)
}

type Measurer interface {
	Space(ctx context.Context, volume string) (free uint64, total uint64, err error)
}

type Server struct {
	Proc    string
	Inspect Inspector
	Resolve Resolver
	Space   Measurer
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
	mux.HandleFunc(http.MethodGet+" "+vars.ValuesPath, s.answer)
	mux.HandleFunc(http.MethodGet+" "+vars.SpacePath, s.measure)
	return mux
}

func (s *Server) manifestOf(w http.ResponseWriter, r *http.Request) (vars.Manifest, bool) {
	held, _ := r.Context().Value(peerKey{}).(peer)
	if held.err != nil {
		http.Error(w, "the caller could not be identified: "+held.err.Error(), http.StatusForbidden)
		return vars.Manifest{}, false
	}
	container, err := s.containerOf(held.pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return vars.Manifest{}, false
	}
	inspecting, cancel := context.WithTimeout(r.Context(), inspectWindow)
	defer cancel()
	raw, err := s.Inspect.Manifest(inspecting, container)
	if err != nil {
		http.Error(w, "read the caller's container: "+err.Error(), http.StatusBadGateway)
		return vars.Manifest{}, false
	}
	if raw == "" {
		http.Error(w, "the caller's container carries no live-value manifest", http.StatusNotFound)
		return vars.Manifest{}, false
	}
	manifest, err := vars.Parse([]byte(raw))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return vars.Manifest{}, false
	}
	if !manifest.Live() {
		http.Error(w, "the caller's manifest names nothing live", http.StatusNotFound)
		return vars.Manifest{}, false
	}
	return manifest, true
}

func (s *Server) measure(w http.ResponseWriter, r *http.Request) {
	manifest, held := s.manifestOf(w, r)
	if !held {
		return
	}
	if manifest.Store == nil || manifest.Store.Volume == "" || s.Space == nil {
		http.Error(w, "the caller's container names no store volume this box holds", http.StatusNotFound)
		return
	}
	free, total, err := s.Space.Space(r.Context(), manifest.Store.Volume)
	if err != nil {
		http.Error(w, "measure "+manifest.Store.Volume+": "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vars.Space{Free: free, Total: total})
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	manifest, held := s.manifestOf(w, r)
	if !held {
		return
	}
	resolved, err := s.Resolve.Resolve(r.Context(), manifest)
	if err != nil {
		http.Error(w, "resolve the caller's values: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vars.Answer{Values: resolved})
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
	host      providerkit.DockerHost
	transport *http.Transport
}

func NewDocker() (*Docker, error) {
	host, err := providerkit.DockerHostFromEnv()
	if err != nil {
		return nil, err
	}
	return &Docker{host: host, transport: host.Transport()}, nil
}

func (d *Docker) Manifest(ctx context.Context, container string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+container+"/json", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: d.transport}).Do(req)
	if err != nil {
		return "", fmt.Errorf("ask the daemon at %s about the container: %w", d.host.Address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("the daemon at %s answered %q about the container: %s", d.host.Address, resp.Status, strings.TrimSpace(string(said)))
	}
	var inspected struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, inspectLimit)).Decode(&inspected); err != nil {
		return "", fmt.Errorf("read what the daemon says about the container: %w", err)
	}
	return ManifestIn(inspected.Config.Env), nil
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
		if value, named := strings.CutPrefix(entry, vars.EnvVar+"="); named {
			return value
		}
	}
	return ""
}

type Store struct {
	ClassRoot    string
	StateRoot    string
	RoutingTable string
}

func (s Store) Resolve(ctx context.Context, manifest vars.Manifest) (map[string]string, error) {
	reader := values.View{
		Records:     vars.Records{Root: s.StateRoot},
		Cipher:      vars.Cipher{Root: s.ClassRoot},
		Scope:       values.Scope{Project: manifest.Slug, Class: providerkit.Class(manifest.Class)},
		Environment: manifest.Environment,
	}
	cells := make([]values.Cell, 0, len(manifest.Keys))
	for _, key := range manifest.Keys {
		cells = append(cells, values.Cell{Folder: key.Folder, Key: key.Key})
	}
	resolved, err := reader.Values(ctx, cells)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		names = append(names, binding.Name)
	}
	records, err := reader.Bindings(ctx, names)
	if err != nil {
		return nil, err
	}
	answer := merged(resolved, manifest.Bindings, records)
	if manifest.Store == nil {
		return answer, nil
	}
	if base := s.storeBase(manifest); base != "" {
		answer[vars.StorePublicKey] = base
		published(answer, manifest.Bindings, base)
	}
	if manifest.Store.Sealed == "" {
		return answer, nil
	}
	secret, err := s.storeSecret(ctx, manifest)
	if err != nil {
		return nil, err
	}
	answer[vars.StoreSecretKey] = secret
	return answer, nil
}

func (s Store) storeBase(manifest vars.Manifest) string {
	if manifest.Store.Pointer == "" {
		return ""
	}
	at := s.RoutingTable
	if at == "" {
		at = vars.RoutingTable
	}
	table, err := os.ReadFile(at)
	if err != nil {
		return ""
	}
	claims, err := vars.ClaimedIn(table)
	if err != nil {
		return ""
	}
	return vars.StoreBase(claims, vars.Surface(manifest.Slug, manifest.Class), manifest.Store.Pointer)
}

func (s Store) storeSecret(ctx context.Context, manifest vars.Manifest) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(manifest.Store.Sealed)
	if err != nil {
		return "", fmt.Errorf("the manifest's %s is not valid base64", vars.StoreSecretName)
	}
	opened, err := (vars.Cipher{Root: s.ClassRoot}).Open(ctx, manifest.StoreCoordinate(), sealed)
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
		held, ok := answer[binding.Key]
		if !ok {
			continue
		}
		record := &bindingsv1.Binding{}
		if err := protojson.Unmarshal([]byte(held), record); err != nil {
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

func merged(resolved map[string]string, bindings []live.Binding, records []values.Published) map[string]string {
	out := make(map[string]string, len(resolved)+len(records))
	maps.Copy(out, resolved)
	for i, record := range records {
		out[bindings[i].Key] = string(record.Value)
	}
	return out
}
