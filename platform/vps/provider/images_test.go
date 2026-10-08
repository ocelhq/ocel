package vps_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const loadedImageRef = "ocel/shop/web:sha256-abc"

type box struct {
	mu         sync.Mutex
	ran        []string
	fed        []string
	hasImage   bool
	unsocket   bool
	drains     bool
	login      string
	serves     map[string]string
	images     map[string]string
	reads      map[string]string
	leaf       string
	kept       string
	routingDoc string
	refuses    func(command string) (session.Result, bool)
	follows    func(ctx context.Context, command string, each func(session.Line) error) error
	forwarded  []string
	stopped    []string
	listening  string
}

func (b *box) ForwardPort(_ context.Context, remote string) (string, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.forwarded = append(b.forwarded, remote)
	if b.listening != "" {
		return b.listening, func() {}, nil
	}
	return fmt.Sprintf("127.0.0.1:%d", 41000+len(b.forwarded)), func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.stopped = append(b.stopped, remote)
	}, nil
}

func (b *box) stoppedForwards() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.stopped)
}

func (b *box) forwardedTo() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.forwarded)
}

func (b *box) Stream(_ context.Context, command string, stdin io.Reader) (session.Result, error) {
	var input string
	if stdin != nil && b.drains {
		if _, err := io.Copy(io.Discard, stdin); err != nil {
			return session.Result{}, err
		}
	} else if stdin != nil {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return session.Result{}, err
		}
		input = string(raw)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, input)
	if b.refuses != nil {
		if result, refused := b.refuses(command); refused {
			return result, nil
		}
	}
	if b.unsocket && strings.HasPrefix(command, "docker version") {
		return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, nil
	}
	if read, named := b.proxying(command, input); named {
		return read, nil
	}
	if read, named := b.catting(command); named {
		return read, nil
	}
	if sealed, sealing := b.sealing(command, input); sealing {
		return sealed, nil
	}
	if strings.Contains(command, "/kept/") {
		if strings.Contains(command, "ln ") && b.kept == "" {
			b.kept = input
		}
		return session.Result{Stdout: b.kept}, nil
	}
	var said string
	for _, line := range strings.Split(command, "\n") {
		fields := strings.Fields(line)
		at := slices.Index(fields, "docker")
		if at < 0 || len(fields)-at < 2 {
			continue
		}
		fields = fields[at:]
		switch {
		case fields[1] == "load":
			b.hasImage = true
			b.name(loadedImageRef, loadedImageRef)
			said = "Loaded image: " + loadedImageRef + "\n"
		case fields[1] == "pull" && len(fields) > 2:
			ref := unquoted(fields[2])
			b.hasImage = true
			b.name(ref, b.served(ref))
			said = "sha256-abc: Pulling from shop/web\nDigest: sha256:abc\nStatus: Downloaded newer image for " + ref + "\n"
		case fields[1] == "tag" && len(fields) > 3:
			b.name(unquoted(fields[3]), b.images[unquoted(fields[2])])
		case fields[1] == "image" && len(fields) > 2 && fields[2] == "ls":
			if b.hasImage {
				said = "sha256:abcdef\n"
			}
		}
	}
	return session.Result{Stdout: said}, nil
}

func (b *box) proxying(command, input string) (session.Result, bool) {
	if !strings.Contains(command, quote(variables.RoutingTable)) {
		return session.Result{}, false
	}
	if b.routingDoc == "" {
		written, err := host.WriteRoutingTable(host.RoutingTable{Grace: host.DrainWindow})
		if err != nil {
			return session.Result{Code: 1, Stderr: err.Error()}, true
		}
		b.routingDoc = string(written)
	}
	switch {
	case strings.Contains(command, "flock -s 9"):
		table, err := host.ReadRoutingTable([]byte(b.routingDoc))
		if err != nil {
			return session.Result{Code: 1, Stderr: err.Error()}, true
		}
		rendered, err := host.RenderProxyConfig(caddy.Builtin{}, table)
		if err != nil {
			return session.Result{Code: 1, Stderr: err.Error()}, true
		}
		encoded := func(read []byte) string { return "+" + base64.StdEncoding.EncodeToString(read) + "\n" }
		return session.Result{Stdout: encoded([]byte(b.routingDoc)) + encoded(rendered)}, true
	case strings.Contains(command, `mv "$staged" `):
		fed, _, _ := strings.Cut(input, "\n")
		written, err := base64.StdEncoding.DecodeString(fed)
		if err != nil {
			return session.Result{Code: 1, Stderr: err.Error()}, true
		}
		b.routingDoc = string(written)
		sum := sha256.Sum256([]byte(b.routingDoc))
		return session.Result{Stdout: hex.EncodeToString(sum[:]) + "\n"}, true
	}
	return session.Result{}, false
}

func (b *box) catting(command string) (session.Result, bool) {
	if named, catted := strings.CutPrefix(command, "cat "); catted && !strings.HasPrefix(named, "| ") {
		var said string
		for _, path := range strings.Fields(named) {
			read, found := b.reads[unquoted(path)]
			if !found {
				return session.Result{Code: 1, Stderr: "cat: " + unquoted(path) + ": No such file or directory"}, true
			}
			said += read
		}
		return session.Result{Stdout: said}, true
	}
	if !strings.Contains(command, "'leaf'") {
		return session.Result{}, false
	}
	if b.leaf == "" {
		return session.Result{Code: 3, Stderr: "ocel-switchboard: 127.0.0.1:443 served no certificate for shop.example.com: EOF"}, true
	}
	return session.Result{Stdout: b.leaf}, true
}

const fakeSeal = "sealed:"

func (b *box) sealing(command, input string) (session.Result, bool) {
	if !strings.Contains(command, boxstore.SealHelper) {
		return session.Result{}, false
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input))
	if err != nil {
		return session.Result{Code: 1, Stderr: "seal: not base64"}, true
	}
	if strings.Contains(command, "'open'") {
		if body, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(string(body), fakeSeal)); err != nil {
			return session.Result{Code: 1, Stderr: "seal: not a value this box sealed"}, true
		}
	} else {
		body = []byte(fakeSeal + base64.StdEncoding.EncodeToString(body))
	}
	return session.Result{Stdout: base64.StdEncoding.EncodeToString(body) + "\n"}, true
}

func (b *box) at(fragment string) int {
	for at, command := range b.commands() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
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

func (b *box) RunLines(ctx context.Context, command string, each func(session.Line) error) error {
	b.mu.Lock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, "")
	follows := b.follows
	b.mu.Unlock()
	if follows == nil {
		return nil
	}
	return follows(ctx, command, each)
}

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
	login := b.login
	if login == "" {
		login = host.DeployUser()
	}
	return session.Destination{Written: "ada@box.invalid", Address: "box.invalid", Port: 22, User: login}
}

func (b *box) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func (b *box) feeds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.fed...)
}

func (b *box) fedTo(needle string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, command := range b.ran {
		if strings.Contains(command, needle) {
			return b.fed[i]
		}
	}
	return ""
}

func directImagesOn(t *testing.T, machine *box) provider.ImageStore {
	t.Helper()
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	store, err := p.OpenDirectImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func daemonServing(t *testing.T, tar string) *int {
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
	t.Setenv(images.DockerTLSVerifyEnv, "")
	t.Setenv(images.DockerCertPathEnv, "")
	t.Setenv(images.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return &reads
}

func aPush(t *testing.T) provider.ImagePush {
	t.Helper()
	return provider.ImagePush{
		App:      "web",
		Source:   "ocel/shop/web@sha256:abc",
		ImageRef: loadedImageRef,
		Built:    wrapped(t),
	}
}

func TestAnImageNothingWrappedIsRefusedRatherThanReadOutOfTheLocalDaemon(t *testing.T) {
	reads := daemonServing(t, "tar-bytes")
	machine := &box{}
	store := directImagesOn(t, machine)

	push := aPush(t)
	push.Built = nil
	err := store.Push(context.Background(), push, nil)
	var rejection refusal.Refusal
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid {
		t.Fatalf("Push() of an unwrapped image = %v, want a refusal: what the local daemon has runs nothing in front of the app", err)
	}
	if *reads != 0 || len(machine.commands()) != 0 {
		t.Errorf("an unwrapped image was read %d times and the machine ran %v", *reads, machine.commands())
	}
}

func TestNothingIsInstalledOnTheMachineToReceiveAnImage(t *testing.T) {
	daemonServing(t, "tar-bytes")
	machine := &box{}
	store := directImagesOn(t, machine)

	if err := store.Push(context.Background(), aPush(t), nil); err != nil {
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

func TestAnImageTheMachineHasIsAnsweredWithoutReadingTheLocalDaemon(t *testing.T) {
	reads := daemonServing(t, "tar-bytes")
	machine := &box{hasImage: true}
	store := directImagesOn(t, machine)

	present, err := store.Has(context.Background(), aPush(t))
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !present {
		t.Fatal("Has() says no over a machine whose daemon answers to the coordinate")
	}
	if *reads != 0 {
		t.Errorf("the local daemon was read %d times answering a question the machine answers", *reads)
	}
}

func TestTheTransferNamesTheMachineRatherThanTheCoordinate(t *testing.T) {
	store := directImagesOn(t, &box{})
	if got := store.Destination(); got != "box.invalid" {
		t.Errorf("Destination() = %q, want the machine the image lands on", got)
	}
}

func wrapped(t *testing.T) v1.Image {
	t.Helper()
	base, err := mutate.Config(empty.Image, v1.Config{Cmd: []string{"/app"}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := images.WrapContainer(base, []byte("#!/bin/sh\nexec \"$@\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestAWrappedImageIsWrittenAsATarballAndLoadedIntoTheMachinesDaemonWithoutReadingTheLocalOne(t *testing.T) {
	reads := daemonServing(t, "tar-bytes")
	machine := &box{}
	store := directImagesOn(t, machine)

	if err := store.Push(context.Background(), aPush(t), nil); err != nil {
		t.Fatalf("Push() of a wrapped image = %v", err)
	}
	if *reads != 0 {
		t.Errorf("the local daemon was read %d times for an image pkg/images already wrapped in memory: what the daemon has is the unwrapped base", *reads)
	}
	if commands := strings.Join(machine.commands(), "\n"); !strings.Contains(commands, "docker load") {
		t.Errorf("the machine ran %q, want the wrapped image loaded into its own daemon", commands)
	}
	var fed string
	for at, command := range machine.commands() {
		if strings.Contains(command, "docker load") {
			fed = machine.feeds()[at]
		}
	}
	archive := tar.NewReader(strings.NewReader(fed))
	var names []string
	for {
		header, err := archive.Next()
		if err != nil {
			break
		}
		names = append(names, header.Name)
	}
	if !slices.Contains(names, "manifest.json") {
		t.Errorf("the machine was fed an archive containing %v, and `docker load` reads a manifest.json out of it", names)
	}
}

func TestAWrappedImagePulledOntoTheMachineIsPinnedToTheDigestOfWhatWasPushed(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	served := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(served.Close)
	server := strings.TrimPrefix(served.URL, "http://")
	store, err := p.OpenRegistryImages(context.Background(), provider.RegistryTarget{Server: server})
	if err != nil {
		t.Fatal(err)
	}
	built := wrapped(t)
	digest, err := built.Digest()
	if err != nil {
		t.Fatal(err)
	}
	push := provider.ImagePush{App: "web", Source: "ocel/shop/web@sha256:abc", ImageRef: server + "/shop/web:sha256-abc-ocel-0123", Built: built}
	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if commands := strings.Join(machine.commands(), "\n"); !strings.Contains(commands, "docker pull "+quote(server+"/shop/web@"+digest.String())) {
		t.Errorf("the machine ran:\n%s\nwant a pull pinned to the digest of the wrapped image, which is what the registry now has", commands)
	}
}

func TestAnImagePulledOntoTheMachineIsRemovedFromTheRegistryItWasPushedTo(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	served := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(served.Close)
	server := strings.TrimPrefix(served.URL, "http://")
	store, err := p.OpenRegistryImages(context.Background(), provider.RegistryTarget{Server: server})
	if err != nil {
		t.Fatal(err)
	}
	push := provider.ImagePush{App: "web", Source: "ocel/shop/web@sha256:abc", ImageRef: server + "/shop/web:sha256-abc-ocel-0123", Built: wrapped(t)}
	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}

	if err := store.Remove(context.Background(), push.ImageRef); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	digest, err := push.Built.Digest()
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := name.NewDigest(server+"/shop/web@"+digest.String(), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Head(pinned); err == nil {
		t.Errorf("the registry still answers for %s after Remove()", pinned)
	}
}

func TestAnImageLoadedOntoTheMachineHasNothingInARegistryToRemove(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if err := directImagesOn(t, machine).Remove(context.Background(), loadedImageRef); err != nil {
		t.Errorf("Remove() = %v, want nothing: an image loaded over SSH lives on the machine alone, which its release window sweeps", err)
	}
	if ran := machine.commands(); len(ran) != 0 {
		t.Errorf("Remove() ran %v on the machine, and the window sweeps it", ran)
	}
}
