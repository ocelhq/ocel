package boxstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var builtAgent = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "ocel-live")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "live")
	build := exec.Command("go", "build", "-o", binary, "./cmd/live")
	build.Dir = filepath.Join("..", "..", "box")
	if said, err := build.CombinedOutput(); err != nil {
		return "", errors.New(string(said))
	}
	return binary, nil
})

type agentHere struct {
	t         *testing.T
	agent     string
	stateRoot string
	tierRoot  string
	ran       []string
}

func newAgentHere(t *testing.T) *agentHere {
	t.Helper()
	agent, err := builtAgent()
	if err != nil {
		t.Fatalf("build ocel-live: %v", err)
	}
	here := &agentHere{t: t, agent: agent, stateRoot: t.TempDir(), tierRoot: t.TempDir()}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := os.MkdirAll(boxapi.KeyValuesDir(here.stateRoot, tier), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(here.tierRoot, string(tier)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return here
}

func (h *agentHere) box() *ssh.Box {
	return ssh.NewBox(func(context.Context) (ssh.Conn, error) { return h, nil }, nil)
}

func (h *agentHere) Stream(ctx context.Context, command string, stdin io.Reader) (ssh.Result, error) {
	h.ran = append(h.ran, command)
	agent := shell.Quote(boxapi.LiveBinary)
	command = strings.Replace(command, agent+" "+shell.Quote(boxapi.RecordsCommand), shell.Quote(h.agent)+" kv --state-root "+shell.Quote(h.stateRoot), 1)
	command = strings.Replace(command, agent+" "+shell.Quote(boxapi.SealCommand), shell.Quote(h.agent)+" seal --tier-root "+shell.Quote(h.tierRoot), 1)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	case errors.As(err, &exited):
		return ssh.Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: exited.ExitCode()}, nil
	default:
		return ssh.Result{}, err
	}
}

func (h *agentHere) RunLines(context.Context, string, func(ssh.Line) error) error { return nil }

func (h *agentHere) Run(ctx context.Context, command string) (string, error) {
	if command == "id -u" {
		return "0\n", nil
	}
	result, err := h.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (h *agentHere) Preflight(context.Context) (ssh.Facts, error) { return ssh.Facts{Root: true}, nil }

func (h *agentHere) Destination() ssh.Destination {
	return ssh.Destination{User: "root", Written: "box"}
}

func (h *agentHere) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("no port is forwarded here")
}

func TestTheRecordsTheProviderKeepsOnTheBoxConformThroughTheBoxAgent(t *testing.T) {
	conformance.RunStore(t, boxstore.NewKeyValues(newAgentHere(t).box()))
}

func TestAnEntryWrittenThroughTheAgentIsTheFileTheBoxReadsInProcess(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")
	revision, err := boxstore.NewKeyValues(here.box()).Write(context.Background(), records.Entry{Key: key, Value: json.RawMessage(`{"parts": ["cache"]}`)})
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	read, err := boxapi.KeyValues{Root: here.stateRoot}.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("the box's own reader = %v", err)
	}
	if read.Revision != revision || string(read.Value) != `{"parts":["cache"]}` {
		t.Errorf("the box reads %s at %s, want the compacted value at %s", read.Value, read.Revision, revision)
	}
}

func TestABoxWithNoAgentKeepsNoRecordsAndRefusesAWriteNamingBootstrap(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	here.agent = filepath.Join(t.TempDir(), "absent")
	store := boxstore.NewKeyValues(here.box())
	key := stackrecords.ProjectKey(environment.TierPreview, "shop")

	if _, err := store.Read(context.Background(), key); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() on a box with no agent = %v, want not found", err)
	}
	if listed, err := store.List(context.Background(), key.Partition); err != nil || len(listed) != 0 {
		t.Errorf("List() on a box with no agent = %v, %v, want nothing", listed, err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: key, Value: json.RawMessage(`{}`)}); err == nil || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("Write() on a box with no agent = %v, want a refusal naming bootstrap", err)
	}
}

func TestAValueTheProviderSealsThroughTheAgentOpensOnTheBoxAndNowhereElse(t *testing.T) {
	t.Parallel()

	here := newAgentHere(t)
	if err := boxapi.CreateKey(here.tierRoot, environment.TierProduction); err != nil {
		t.Fatal(err)
	}
	cipher := boxstore.NewCipher(here.box())
	bound := seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

	sealed, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("postgres://example"))
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	opened, err := boxapi.Cipher{Root: here.tierRoot}.Open(context.Background(), environment.TierProduction, bound, sealed)
	if err != nil || string(opened) != "postgres://example" {
		t.Fatalf("the box opens %q, %v, want the value sealed", opened, err)
	}
	if _, err := cipher.Open(context.Background(), environment.TierProduction, seal.BindToProject("other", "variable", "production", "*", "/", "DATABASE_URL"), sealed); err == nil {
		t.Error("a value sealed for shop opened for other")
	}
	if want := shell.Words(boxstore.SealArgv(environment.TierProduction, boxapi.SealVerbSeal, bound)); here.ran[0] != want {
		t.Errorf("the seal ran as %q, want %q: the sudoers line names exactly that command", here.ran[0], want)
	}
}
