package enginetest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
)

const (
	engine = "docker"

	runLabel   = "ocel.test.run"
	rootPrefix = "ocel-test-"
	runFile    = "run"
)

var (
	run     = runOf(hostname(), os.Getpid())
	engaged atomic.Bool
	begun   sync.Once
	silent  string
)

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unnamed"
	}
	return name
}

func runOf(host string, pid int) string { return host + "/" + strconv.Itoa(pid) }

func Main(m *testing.M) int {
	code := m.Run()
	if !engaged.Load() {
		return code
	}
	if err := sweep(func(of string) bool { return of == run }); err != nil {
		fmt.Fprintf(os.Stderr, "this run leaves behind what it started on the engine, and every run after it inherits the leftovers:\n%v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func RunLabelArgs(t *testing.T) []string {
	t.Helper()
	requireDocker(t)
	return labelledAs(run)
}

func labelledAs(of string) []string { return []string{"--label", runLabel + "=" + of} }

func requireDocker(t *testing.T) {
	t.Helper()
	begun.Do(func() {
		if _, err := exec.LookPath(engine); err != nil {
			silent = "this machine has no docker, so nothing can run on an engine that is not here"
			return
		}
		if err := exec.Command(engine, "info").Run(); err != nil {
			silent = "the docker on this machine answers nothing, so nothing can run on it"
			return
		}
		engaged.Store(true)
		if err := sweep(runProcessGone); err != nil {
			fmt.Fprintf(os.Stderr, "a run that ended without clearing up left what this one could not take either:\n%v\n", err)
		}
	})
	if silent != "" {
		t.Skip(silent)
	}
}

func runProcessGone(of string) bool {
	at := strings.LastIndex(of, "/")
	if at < 0 || of == run || of[:at] != hostname() {
		return false
	}
	pid, err := strconv.Atoi(of[at+1:])
	if err != nil || pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return errors.Is(process.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

func sweep(owned func(of string) bool) error {
	var failed []error
	containers, err := listLabelled("ps", "--all")
	if err != nil {
		return err
	}
	if taken := ownedBy(containers, owned); len(taken) > 0 {
		failed = append(failed, docker(append([]string{"rm", "--force", "--volumes"}, taken...)...))
	}
	networks, err := listLabelled("network", "ls")
	if err != nil {
		return errors.Join(append(failed, err)...)
	}
	for _, network := range ownedBy(networks, owned) {
		failed = append(failed, removeNetwork(network))
	}
	for _, root := range roots(owned) {
		failed = append(failed, removeRunRoot(root))
	}
	return errors.Join(failed...)
}

func listLabelled(listing ...string) (map[string]string, error) {
	argv := append(slices.Clone(listing), "--filter", "label="+runLabel, "--format", `{{.ID}} {{.Label "`+runLabel+`"}}`)
	said, err := exec.Command(engine, argv...).Output()
	if err != nil {
		return nil, fmt.Errorf("list what runs started (%s): %w", strings.Join(listing, " "), err)
	}
	found := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(said)), "\n") {
		if id, of, ok := strings.Cut(line, " "); ok {
			found[id] = of
		}
	}
	return found, nil
}

func ownedBy(found map[string]string, owned func(of string) bool) []string {
	var taken []string
	for id, of := range found {
		if owned(of) {
			taken = append(taken, id)
		}
	}
	return taken
}

func removeNetwork(network string) error {
	said, err := exec.Command(engine, "network", "inspect", "--format", `{{range .Containers}}{{.Name}} {{end}}`, network).Output()
	if err != nil {
		return fmt.Errorf("read what is attached to network %s: %w", network, err)
	}
	if attached := strings.Fields(string(said)); len(attached) > 0 {
		if err := docker(append([]string{"rm", "--force", "--volumes"}, attached...)...); err != nil {
			return err
		}
	}
	return docker("network", "rm", network)
}

func docker(argv ...string) error {
	if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", engine, strings.Join(argv, " "), err, strings.TrimSpace(string(said)))
	}
	return nil
}

func runRootParents() []string {
	found := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil && home != os.TempDir() {
		found = append(found, home)
	}
	return found
}

func roots(owned func(of string) bool) []string {
	var found []string
	for _, base := range runRootParents() {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), rootPrefix) {
				continue
			}
			root := filepath.Join(base, entry.Name())
			of, err := os.ReadFile(filepath.Join(root, runFile))
			if err == nil && owned(string(of)) {
				found = append(found, root)
			}
		}
	}
	return found
}

func removeRunRoot(root string) error {
	if emptyRunRoot(root) == nil {
		return os.RemoveAll(root)
	}
	said, err := exec.Command(engine, "run", "--rm", "--network", "none", "--user", "0",
		"--volume", root+":/reclaimed", "--entrypoint", "find", constants.ObjectStoreImage(),
		"/reclaimed", "-mindepth", "1", "!", "-path", "/reclaimed/"+runFile, "-delete").CombinedOutput()
	if err != nil {
		if _, gone := os.Stat(root); errors.Is(gone, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("take back %s, which contains what a container wrote as a user this run is not: %w\n%s", root, err, strings.TrimSpace(string(said)))
	}
	if err := emptyRunRoot(root); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func emptyRunRoot(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failed []error
	for _, entry := range entries {
		if entry.Name() != runFile {
			failed = append(failed, os.RemoveAll(filepath.Join(root, entry.Name())))
		}
	}
	return errors.Join(failed...)
}

func uniqueName(what string) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		panic(err)
	}
	return rootPrefix + what + "-" + hex.EncodeToString(suffix[:])
}

type onceOrSkip[T any] struct {
	once    sync.Once
	value   T
	refused string
}

func (l *onceOrSkip[T]) get(t *testing.T, provision func() (T, string)) T {
	t.Helper()
	l.once.Do(func() { l.value, l.refused = provision() })
	if l.refused != "" {
		t.Skip(l.refused)
	}
	return l.value
}

var network onceOrSkip[string]

func Network(t *testing.T) string {
	t.Helper()
	labels := RunLabelArgs(t)
	return network.get(t, func() (string, string) {
		name := uniqueName("net")
		argv := append(append([]string{"network", "create"}, labels...), name)
		if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
			return "", fmt.Sprintf("this machine's engine will not create the network the run's containers resolve each other across: %s", said)
		}
		return name, ""
	})
}

var bound onceOrSkip[string]

func BindSource(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	root := bound.get(t, rootSeenByTheEngine)
	dir, err := os.MkdirTemp(root, "test-")
	if err != nil {
		t.Fatalf("make a bind source under %s: %v", root, err)
	}
	return dir
}

func rootSeenByTheEngine() (string, string) {
	var refused []string
	for _, base := range runRootParents() {
		root, err := os.MkdirTemp(base, rootPrefix)
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		if err := os.WriteFile(filepath.Join(root, runFile), []byte(run), 0o644); err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", base, err))
			_ = os.RemoveAll(root)
			continue
		}
		said, err := exec.Command(engine, "run", "--rm", "--network", "none", "--user", "0",
			"--volume", root+":/seen:ro", "--entrypoint", "test", constants.ObjectStoreImage(),
			"-f", "/seen/"+runFile).CombinedOutput()
		if err == nil {
			return root, ""
		}
		_ = os.RemoveAll(root)
		refused = append(refused, fmt.Sprintf("%s: %s", base, strings.TrimSpace(string(said))))
	}
	return "", "this machine's engine can read no directory this run can write, so no bind source can be handed to it: " + strings.Join(refused, "; ")
}
