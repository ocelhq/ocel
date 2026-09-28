package host

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
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type helperHere struct {
	t    *testing.T
	root string
}

func newHelperHere(t *testing.T) helperHere {
	t.Helper()
	root := helperDir(t)
	if err := os.MkdirAll(filepath.Join(root, string(environment.TierPreview), "records"), 0o750); err != nil {
		t.Fatal(err)
	}
	return helperHere{t: t, root: root}
}

func (h helperHere) HasStore(context.Context, environment.Tier) (bool, error) { return true, nil }

func (h helperHere) KeyValues(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
	script := filepath.Join(h.t.TempDir(), "keyvalues")
	if err := os.WriteFile(script, keyValuesScript, 0o755); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{script, string(tier)}, argv...)...)
	cmd.Env = append(os.Environ(), "OCEL_STATE_ROOT="+h.root)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(rendered), nil
	case errors.As(err, &exit) && exit.ExitCode() == boxstore.ExitNotFound:
		return "", keyvalue.ErrNotFound
	case errors.As(err, &exit) && exit.ExitCode() == boxstore.ExitStale:
		return "", keyvalue.ErrStale
	default:
		return "", refusal.Refuse(refusal.CodeDenied, "keyvalues %s: %v: %s", argv[0], err, stderr.String())
	}
}

func TestTheBoxStoreConformsWhereItsHelperRuns(t *testing.T) {
	conformance.RunStore(t, boxstore.NewKeyValues(newHelperHere(t)))
}

func TestAnEntryOnTheBoxIsAJSONFileHoldingTheValueAsWritten(t *testing.T) {
	t.Parallel()

	here := newHelperHere(t)
	store := boxstore.NewKeyValues(here)
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")
	revision, err := store.Write(context.Background(), keyvalue.Entry{Key: key, Value: json.RawMessage(`{"features": ["cache"]}`)})
	if err != nil {
		t.Fatal(err)
	}

	path, err := live.PathOf(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(live.KeyValuesDir(here.root, environment.TierProduction), path+live.EntrySuffix))
	if err != nil {
		t.Fatalf("read the entry's file: %v", err)
	}
	var file struct {
		Revision string          `json:"revision"`
		Value    json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("the entry's file holds %q, which is not JSON: %v", raw, err)
	}
	if file.Revision != string(revision) || string(file.Value) != `{"features":["cache"]}` {
		t.Errorf("the entry's file holds revision %q and value %s, want %q and the value written", file.Revision, file.Value, revision)
	}

	read, err := (live.KeyValues{Root: here.root}).Read(context.Background(), key)
	if err != nil || read.Revision != revision || string(read.Value) != `{"features":["cache"]}` {
		t.Errorf("the box-side reader answered %+v, %v, want the entry the helper wrote", read, err)
	}
}

func TestASchemaTheOlderLayoutWroteIsRefusedRatherThanStampedOver(t *testing.T) {
	t.Parallel()

	here := newHelperHere(t)
	tier := environment.TierPreview
	path, err := live.PathOf(stackrecords.SchemaKey(tier))
	if err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(live.KeyValuesDir(here.root, tier), path)
	if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at+".rec", []byte("0123456789abcdef0123456789abcdef\nMg==\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	err = stackrecords.EnsureSchema(context.Background(), boxstore.NewKeyValues(here), tier)
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "older ocel") {
		t.Fatalf("EnsureSchema() over a schema the older layout wrote = %v, want a refusal saying an older ocel wrote it: a build that reads it as unwritten stamps its own schema beside entries it cannot see", err)
	}
	if _, err := os.Stat(at + live.EntrySuffix); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("EnsureSchema() left %s%s behind (%v), want the older schema the only one there", at, live.EntrySuffix, err)
	}
}
