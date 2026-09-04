package vps_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	pullUsername = "acme-bot"
	pullPassword = "hunter2"
	pullDigest   = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	pullTag      = "sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type registryLog struct {
	mu     sync.Mutex
	asked  []string
	basics []string
	holds  bool
}

func (r *registryLog) held(holds bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holds = holds
}

func (r *registryLog) reads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

func (r *registryLog) presented() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.basics...)
}

func standingRegistry(t *testing.T) (string, *registryLog) {
	t.Helper()
	log := &registryLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if authorization == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="ocel"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		log.mu.Lock()
		log.asked = append(log.asked, r.URL.Path)
		log.basics = append(log.basics, authorization)
		holds := log.holds
		log.mu.Unlock()
		if !holds {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", pullDigest)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), log
}

type daemonLog struct {
	mu     sync.Mutex
	named  []string
	pushed []string
}

func (d *daemonLog) pushes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.pushed...)
}

func standingDaemon(t *testing.T) *daemonLog {
	t.Helper()
	log := &daemonLog{}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		log.mu.Lock()
		switch {
		case strings.HasSuffix(path, "/tag"):
			log.named = append(log.named, path)
		case strings.HasSuffix(path, "/push"):
			log.pushed = append(log.pushed, path)
		}
		log.mu.Unlock()
		switch {
		case strings.HasSuffix(path, "/tag"):
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/push"):
			_, _ = io.WriteString(w, `{"status":"Pushed"}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv(providerkit.DockerTLSVerifyEnv, "")
	t.Setenv(providerkit.DockerCertPathEnv, "")
	t.Setenv(providerkit.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return log
}

func provisioning(t *testing.T, machine *box) *vps.Provider {
	t.Helper()
	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func pulling(t *testing.T, machine *box, target providerkit.RegistryTarget) providerkit.ImageStore {
	t.Helper()
	store, err := provisioning(t, machine).Images(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func aTarget(server string) providerkit.RegistryTarget {
	return providerkit.RegistryTarget{
		Server:    server,
		Namespace: "acme",
		Username:  pullUsername,
		Password:  pullPassword,
	}
}

func aPull(target providerkit.RegistryTarget) providerkit.ImagePush {
	return providerkit.ImagePush{
		App:    "web",
		Source: "ocel/shop/web@" + pullDigest,
		Target: target.Coordinate("web", pullTag),
		Digest: pullDigest,
	}
}

func TestARegistryTurnsTheTransferIntoAPullTheMachineMakes(t *testing.T) {
	standingDaemon(t)
	server, _ := standingRegistry(t)
	machine := &box{}
	target := aTarget(server)
	push := aPull(target)

	if err := pulling(t, machine, target).Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}

	commands := strings.Join(machine.commands(), "\n")
	pinned := pinnedAt(push.Target, pullDigest)
	if !strings.Contains(commands, "docker pull "+quotedIn(pinned)) {
		t.Errorf("the machine ran %q, want it to pull %s", commands, pinned)
	}
	if !strings.Contains(commands, "docker tag "+quotedIn(pinned)+" "+quotedIn(push.Target)) {
		t.Errorf("the machine ran %q, want the digest it pulled named %s", commands, push.Target)
	}
	if strings.Contains(commands, "docker load") {
		t.Errorf("the machine ran %q: a registry is named, so nothing is streamed onto the box", commands)
	}
	for _, carried := range machine.carried() {
		if strings.Contains(carried, "tar") {
			t.Errorf("the machine was fed %q where it was told to pull", carried)
		}
	}
}

func TestAnImageServedUnderTheTagIsNotWhatTheMachineEndsHolding(t *testing.T) {
	standingDaemon(t)
	server, registry := standingRegistry(t)
	registry.held(true)
	target := aTarget(server)
	push := aPull(target)
	machine := &box{serves: map[string]string{
		push.Target:                       "an image someone else wrote to the tag",
		pinnedAt(push.Target, pullDigest): "the image this deploy built",
	}}

	if err := pulling(t, machine, target).Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if held := machine.under(push.Target); held != "the image this deploy built" {
		t.Errorf("the machine holds %q under %s, and the release loop, rollback and retention all pin that coordinate: "+
			"a tag is rewritten by whoever can write the registry, and only the digest names the image this deploy built", held, push.Target)
	}
}

func TestTheLoginTakesThePasswordOnStdinAndLogsOutInTheSameSession(t *testing.T) {
	standingDaemon(t)
	server, _ := standingRegistry(t)
	machine := &box{}
	target := aTarget(server)

	if err := pulling(t, machine, target).Push(context.Background(), aPull(target), nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}

	pulled := sessionThatPulls(t, machine)
	if !strings.Contains(pulled, "--password-stdin") {
		t.Errorf("the pull ran %q, want the login to take the password on stdin", pulled)
	}
	if strings.Contains(pulled, pullPassword) {
		t.Errorf("the pull ran %q, and the password is written into a command line every process on the machine can read", pulled)
	}
	if !strings.Contains(pulled, "docker logout "+quotedIn(server)) {
		t.Errorf("the pull ran %q, want the login given back before the session ends", pulled)
	}
	if fed := strings.Join(machine.carried(), ""); fed != pullPassword {
		t.Errorf("the machine was fed %q, want the registry password and nothing else", fed)
	}
}

func TestNoCredentialFileIsLeftBehindOnTheMachine(t *testing.T) {
	standingDaemon(t)
	server, _ := standingRegistry(t)
	machine := &box{}
	target := aTarget(server)

	if err := pulling(t, machine, target).Push(context.Background(), aPull(target), nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}

	pulled := sessionThatPulls(t, machine)
	if !strings.Contains(pulled, "DOCKER_CONFIG=") {
		t.Errorf("the pull ran %q, so the login lands in the deploy login's own docker config and stays there", pulled)
	}
	if !strings.Contains(pulled, "mktemp -d") || !strings.Contains(pulled, "rm -rf") {
		t.Errorf("the pull ran %q, want the config it writes made and taken away within the session", pulled)
	}
}

func TestTheCredentialDirectoryGoesEvenWhereTheSessionRefuses(t *testing.T) {
	standingDaemon(t)
	server, registry := standingRegistry(t)
	registry.held(true)
	machine, asked := refusingPulls("Error response from daemon: manifest unknown", 100)
	target := aTarget(server)

	if err := pulling(t, machine, target).Push(context.Background(), aPull(target), nil); err == nil {
		t.Fatal("Push() = nil over a machine whose pull refused, so a release would promote an image the box was never given")
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("the machine was told to pull %d times over a manifest the registry does not hold, want the refusal taken at its word", got)
	}
	underShell(t, sessionThatPulls(t, machine))
}

func underShell(t *testing.T, script string) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no shell here to run the session the machine would run")
	}
	dir := t.TempDir()
	bin, tmp := filepath.Join(dir, "bin"), filepath.Join(dir, "tmp")
	for _, made := range []string{bin, tmp} {
		if err := os.Mkdir(made, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	refusing := "#!/bin/sh\ncase \"$1\" in\n" +
		"login) cat >/dev/null; exit 0;;\n" +
		"pull) echo 'manifest unknown' >&2; exit 1;;\n" +
		"logout) exit 1;;\n" +
		"*) exit 0;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(refusing), 0o700); err != nil {
		t.Fatal(err)
	}

	ran := exec.Command(shell, "-c", script)
	ran.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TMPDIR="+tmp)
	ran.Stdin = strings.NewReader(pullPassword)
	if err := ran.Run(); err == nil {
		t.Fatal("the session came back clean over a pull the daemon refused")
	}
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the session left %d entries under its temporary directory, and the docker config among them "+
			"carries the registry username and password in the clear for as long as the machine stands", len(left))
	}
}

func TestAPulledImageAnswersToTheCoordinateTheCliOwns(t *testing.T) {
	standingDaemon(t)
	server, _ := standingRegistry(t)
	machine := &box{}
	target := aTarget(server)
	push := aPull(target)
	store := pulling(t, machine, target)

	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held {
		t.Fatal("the machine answers to no coordinate after a pull, so release, rollback and retention have nothing to pin")
	}
	if !strings.Contains(strings.Join(machine.commands(), "\n"), quotedIn(push.Target)) {
		t.Errorf("the machine was never asked about %s, and a loaded image answers to that coordinate verbatim", push.Target)
	}
}

func TestADigestTheMachineAlreadyHoldsIsNeitherPushedNorPulledAgain(t *testing.T) {
	daemon := standingDaemon(t)
	server, registry := standingRegistry(t)
	machine := &box{holds: true}
	target := aTarget(server)
	plan := providerkit.ImagePlan{
		Store:  pulling(t, machine, target),
		Pushes: []providerkit.ImagePush{aPull(target)},
	}

	if err := plan.Ship(context.Background(), nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	if pushed := daemon.pushes(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v for an image the machine already holds", pushed)
	}
	if commands := strings.Join(machine.commands(), "\n"); strings.Contains(commands, "docker pull") {
		t.Errorf("the machine ran %q over a digest it already holds", commands)
	}
	if len(registry.reads()) != 0 {
		t.Errorf("the registry was read %v answering a question the machine answers", registry.reads())
	}
}

func TestAMachineMissingADigestTheRegistryHoldsPullsWithoutASecondPush(t *testing.T) {
	daemon := standingDaemon(t)
	server, registry := standingRegistry(t)
	registry.held(true)
	machine := &box{}
	target := aTarget(server)
	push := aPull(target)

	if err := pulling(t, machine, target).Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if pushed := daemon.pushes(); len(pushed) != 0 {
		t.Errorf("the deploy pushed %v that the registry already holds", pushed)
	}
	if !strings.Contains(strings.Join(machine.commands(), "\n"), "docker pull") {
		t.Error("the registry held the digest and the machine was never told to pull it, so the box serves an image it does not have")
	}
	if presented := registry.presented(); len(presented) == 0 || presented[0] != basicFor(pullUsername, pullPassword) {
		t.Errorf("the registry was read as %v, want the deploy's own credentials", presented)
	}
}

func TestAPasswordWithNoLoginNameIsRefusedBeforeTheImageIsPublished(t *testing.T) {
	daemon := standingDaemon(t)
	server, registry := standingRegistry(t)
	machine := &box{}
	target := aTarget(server)
	target.Username = ""

	ctx := context.Background()
	store, err := provisioning(t, machine).Images(ctx, target)
	if err == nil {
		err = store.Push(ctx, aPull(target), nil)
	}
	if err == nil {
		t.Fatal("the deploy took a registry password no login name goes with, so the machine pulled anonymously from a private registry")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("the refusal reads %v, want the missing login name named", err)
	}
	if pushed := daemon.pushes(); len(pushed) != 0 {
		t.Errorf("the deploy published %v and refused afterwards, so the image stands in a registry over a credential ocel would not use", pushed)
	}
	if reads := registry.reads(); len(reads) != 0 {
		t.Errorf("the registry was reached as %v under a password with no login name to present it under", reads)
	}
}

func refusingPulls(said string, times int64) (*box, *atomic.Int64) {
	var asked atomic.Int64
	machine := &box{}
	machine.refuses = func(command string) (session.Result, bool) {
		if !strings.Contains(command, "docker pull") {
			return session.Result{}, false
		}
		return session.Result{Code: 1, Stderr: said}, asked.Add(1) <= times
	}
	return machine, &asked
}

func TestAThrottledPullIsAskedAgainRatherThanFailingTheDeploy(t *testing.T) {
	standingDaemon(t)
	server, registry := standingRegistry(t)
	registry.held(true)
	machine, asked := refusingPulls("toomanyrequests: You have reached your pull rate limit", 2)
	target := aTarget(server)

	if err := pulling(t, machine, target).Push(context.Background(), aPull(target), nil); err != nil {
		t.Fatalf("Push() = %v over a registry that throttled the first pulls and then served the image", err)
	}
	if got := asked.Load(); got != 3 {
		t.Errorf("the machine was told to pull %d times, want the throttled attempts asked again: "+
			"the same 429 is retried where ocel pushes and fatal where the machine pulls", got)
	}
}

func TestAPullTheRegistryDeniesIsNotAskedAgain(t *testing.T) {
	standingDaemon(t)
	server, registry := standingRegistry(t)
	registry.held(true)
	machine, asked := refusingPulls("unauthorized: authentication required", 100)
	target := aTarget(server)

	if err := pulling(t, machine, target).Push(context.Background(), aPull(target), nil); err == nil {
		t.Fatal("Push() = nil over a registry that refused the machine's login")
	}
	if got := asked.Load(); got != 1 {
		t.Errorf("the machine was told to pull %d times over a credential the registry refuses, want the refusal taken at its word", got)
	}
}

func TestThePullNamesTheMachineRatherThanTheRegistry(t *testing.T) {
	standingDaemon(t)
	server, _ := standingRegistry(t)
	store := pulling(t, &box{}, aTarget(server))

	named, says := store.(providerkit.ImageDestination)
	if !says {
		t.Fatal("the pulling store does not say where the image lands, so a deploy reports the registry as though it were the destination")
	}
	if got := named.ImageDestination(); got != "box.invalid" {
		t.Errorf("ImageDestination() = %q, want the machine the image lands on", got)
	}
}

func basicFor(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func quotedIn(arg string) string { return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'" }

func pinnedAt(coordinate, digest string) string {
	return coordinate[:strings.LastIndex(coordinate, ":")] + "@" + digest
}

func sessionThatPulls(t *testing.T, machine *box) string {
	t.Helper()
	for _, command := range machine.commands() {
		if strings.Contains(command, "docker pull") {
			return command
		}
	}
	t.Fatal("the machine was never told to pull")
	return ""
}
