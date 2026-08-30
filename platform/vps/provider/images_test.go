package vps_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const loadedCoordinate = "ocel/web:sha256-abc"

type box struct {
	mu       sync.Mutex
	ran      []string
	fed      []string
	holds    bool
	unsocket bool
	serves   map[string]string
	images   map[string]string
	refuses  func(command string) (session.Result, bool)
}

func (b *box) Stream(_ context.Context, command string, stdin io.Reader) (session.Result, error) {
	var carried string
	if stdin != nil {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return session.Result{}, err
		}
		carried = string(raw)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, carried)
	if b.refuses != nil {
		if result, refused := b.refuses(command); refused {
			return result, nil
		}
	}
	if b.unsocket && strings.Contains(command, "docker version") {
		return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, nil
	}
	var said string
	for _, line := range strings.Split(command, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "docker" {
			continue
		}
		switch {
		case fields[1] == "load":
			b.holds = true
			b.name(loadedCoordinate, loadedCoordinate)
			said = "Loaded image: " + loadedCoordinate + "\n"
		case fields[1] == "pull" && len(fields) > 2:
			ref := unquoted(fields[2])
			b.holds = true
			b.name(ref, b.served(ref))
			said = "Status: Downloaded newer image\n"
		case fields[1] == "tag" && len(fields) > 3:
			b.name(unquoted(fields[3]), b.images[unquoted(fields[2])])
		case fields[1] == "image" && len(fields) > 2 && fields[2] == "ls":
			if b.holds {
				said = "sha256:abcdef\n"
			}
		}
	}
	return session.Result{Stdout: said}, nil
}

func (b *box) name(ref, image string) {
	if b.images == nil {
		b.images = map[string]string{}
	}
	b.images[ref] = image
}

func (b *box) served(ref string) string {
	if image, serves := b.serves[ref]; serves {
		return image
	}
	return ref
}

func (b *box) under(ref string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.images[ref]
}

func unquoted(field string) string { return strings.Trim(field, "'") }

func (b *box) Run(ctx context.Context, command string) (string, error) {
	result, err := b.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (b *box) Preflight(context.Context) (session.Facts, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return session.Facts{Root: !b.unsocket, Systemd: true}, nil
}

func (b *box) Destination() session.Destination {
	return session.Destination{Written: "ada@box.invalid", Address: "box.invalid", Port: 22, User: "ada"}
}

func (b *box) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func (b *box) carried() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.fed...)
}

func standing(t *testing.T, machine *box) providerkit.ImageStore {
	t.Helper()
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	store, err := p.DirectImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func daemonHolding(t *testing.T, tar string) *int {
	t.Helper()
	var reads int
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/get") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reads++
		_, _ = io.WriteString(w, tar)
	}))
	t.Cleanup(daemon.Close)
	t.Setenv(providerkit.DockerTLSVerifyEnv, "")
	t.Setenv(providerkit.DockerCertPathEnv, "")
	t.Setenv(providerkit.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return &reads
}

func aPush() providerkit.ImagePush {
	return providerkit.ImagePush{
		App:    "web",
		Source: "ocel/web@sha256:abc",
		Target: loadedCoordinate,
		Digest: "sha256:abc",
	}
}

func TestTheImageIsReadOutOfTheLocalDaemonAndPipedIntoTheMachinesOwn(t *testing.T) {
	reads := daemonHolding(t, "tar-bytes")
	machine := &box{}
	store := standing(t, machine)

	if err := store.Push(context.Background(), aPush(), nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if *reads != 1 {
		t.Errorf("the local daemon was read %d times for one image", *reads)
	}
	if carried := strings.Join(machine.carried(), "\n"); !strings.Contains(carried, "tar-bytes") {
		t.Errorf("the machine was fed %q, want the tar the local daemon handed over", carried)
	}
	if commands := strings.Join(machine.commands(), "\n"); !strings.Contains(commands, "docker load") {
		t.Errorf("the machine ran %q, want the stream loaded into its own daemon", commands)
	}
}

func TestNothingIsInstalledOnTheMachineToReceiveAnImage(t *testing.T) {
	daemonHolding(t, "tar-bytes")
	machine := &box{}
	store := standing(t, machine)

	if err := store.Push(context.Background(), aPush(), nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	for _, command := range machine.commands() {
		for _, writing := range []string{"apt-get", "curl", "wget", "install", "mkdir", "tee", "cat >", "systemctl"} {
			if strings.Contains(command, writing) {
				t.Errorf("the transfer ran %q on the machine: the bootstrap guarantee it consumes is dockerd and nothing else", command)
			}
		}
	}
}

func TestAnImageTheMachineHoldsIsAnsweredWithoutReadingTheLocalDaemon(t *testing.T) {
	reads := daemonHolding(t, "tar-bytes")
	machine := &box{holds: true}
	store := standing(t, machine)

	held, err := store.Has(context.Background(), aPush())
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held {
		t.Fatal("Has() says no over a machine whose daemon answers to the coordinate")
	}
	if *reads != 0 {
		t.Errorf("the local daemon was read %d times answering a question the machine answers", *reads)
	}
}

func TestTheTransferNamesTheMachineRatherThanTheCoordinate(t *testing.T) {
	store := standing(t, &box{})
	named, says := store.(providerkit.ImageDestination)
	if !says {
		t.Fatal("the direct store does not say where it sends, so a deploy reports the coordinate as though it were a destination")
	}
	if got := named.ImageDestination(); got != "box.invalid" {
		t.Errorf("ImageDestination() = %q, want the machine the image lands on", got)
	}
}
