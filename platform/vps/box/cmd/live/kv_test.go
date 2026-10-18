package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func kv(t *testing.T, root, stdin string, argv ...string) ran {
	t.Helper()
	var stdout, stderr strings.Builder
	code := recordsCommand(append([]string{"--state-root", root}, argv...), strings.NewReader(stdin), &stdout, &stderr)
	return ran{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func stateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(boxapi.KeyValuesDir(root, environment.TierProduction), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func revisionsOf(t *testing.T, got ran) []string {
	t.Helper()
	if got.code != 0 || !strings.HasSuffix(got.stdout, "]\n") {
		t.Fatalf("cas = %+v, want exit 0 and one JSON array", got)
	}
	var revisions []string
	if err := json.Unmarshal([]byte(got.stdout), &revisions); err != nil {
		t.Fatalf("cas printed %q: %v", got.stdout, err)
	}
	return revisions
}

func TestTheRecordsCommandComparesAndSetsThenGetsWhatTheFileHolds(t *testing.T) {
	t.Parallel()
	root := stateRoot(t)

	revisions := revisionsOf(t, kv(t, root, `[{"path":"p/a","revision":"","value":{ "url": "a<b" }}]`, "production", "cas"))
	if len(revisions) != 1 || len(revisions[0]) != 32 {
		t.Fatalf("cas = %q, want one revision", revisions)
	}
	file, err := os.ReadFile(filepath.Join(boxapi.KeyValuesDir(root, environment.TierProduction), "p/a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"revision":"` + revisions[0] + `","value":{"url":"a<b"}}` + "\n"; string(file) != want {
		t.Errorf("the entry's file holds %q, want %q", file, want)
	}
	if got := kv(t, root, "", "production", "get", "p/a"); got.code != 0 || got.stdout != string(file) {
		t.Errorf("get = %+v, want the entry file's line %q", got, file)
	}

	if got := kv(t, root, `[{"path":"p/a","revision":"","value":2}]`, "production", "cas"); got.code != 4 || got.stdout != "" {
		t.Errorf("cas expecting no entry over one = %+v, want exit 4", got)
	}
	swapped := revisionsOf(t, kv(t, root,
		`[{"path":"p/a","revision":"`+revisions[0]+`","remove":true},{"path":"p/b","revision":"","value":2}]`, "production", "cas"))
	if len(swapped) != 2 || swapped[0] != "" || len(swapped[1]) != 32 {
		t.Errorf("cas of a removal and a write = %q, want an empty revision then a new one", swapped)
	}
	if got := kv(t, root, "", "production", "get", "p/a"); got.code != 3 || got.stdout != "" {
		t.Errorf("get of a removed entry = %+v, want exit 3", got)
	}
	if got := kv(t, root, `[{"path":"p/a","revision":"","remove":true}]`, "production", "cas"); got.code != 3 {
		t.Errorf("cas removing an absent entry = %+v, want exit 3", got)
	}
	if got := kv(t, root, `[{"path":"p/b","revision":"`+swapped[1]+`","value":3},{"path":"p/c","revision":"0123456789abcdef0123456789abcdef","value":3}]`, "production", "cas"); got.code != 4 {
		t.Errorf("cas whose second change is stale = %+v, want exit 4", got)
	}
	if got := kv(t, root, "", "production", "get", "p/b"); got.code != 0 || !strings.Contains(got.stdout, `"revision":"`+swapped[1]+`"`) {
		t.Errorf("get after a refused cas = %+v, want the first entry untouched", got)
	}
}

func TestTheRecordsCommandListsEachEntryUnderAPartitionAsAJSONLine(t *testing.T) {
	t.Parallel()
	root := stateRoot(t)
	revisionsOf(t, kv(t, root, `[{"path":"p/t/a","revision":"","value":"a"},{"path":"p/t/b/c","revision":"","value":"c"}]`, "production", "cas"))
	revisionsOf(t, kv(t, root, `[{"path":"q/t/a","revision":"","value":"elsewhere"}]`, "production", "cas"))

	for under, want := range map[string][]string{
		"":    {"t/a", "t/b/c"},
		"t":   {"t/a", "t/b/c"},
		"t/b": {"t/b/c"},
		"t/a": {"t/a"},
		"t/z": nil,
	} {
		argv := []string{"production", "list", "p"}
		if under != "" {
			argv = append(argv, under)
		}
		got := kv(t, root, "", argv...)
		if got.code != 0 {
			t.Fatalf("list %q = %+v", argv, got)
		}
		var paths []string
		for _, line := range strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n") {
			if line == "" {
				continue
			}
			var entry struct {
				Path     string          `json:"path"`
				Revision string          `json:"revision"`
				Value    json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil || len(entry.Revision) != 32 || len(entry.Value) == 0 {
				t.Fatalf("list %q printed the line %q, %v", argv, line, err)
			}
			paths = append(paths, entry.Path)
		}
		if strings.Join(paths, ",") != strings.Join(want, ",") {
			t.Errorf("list %q = %q, want %q", argv, paths, want)
		}
	}
}

func TestTheRecordsCommandRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	root := stateRoot(t)
	for _, call := range []struct {
		stdin string
		argv  []string
	}{
		{"", nil},
		{"", []string{"production"}},
		{"", []string{"../production", "get", "p/a"}},
		{"", []string{"production", "read", "p/a"}},
		{"", []string{"production", "get"}},
		{"", []string{"production", "get", "p/../../a"}},
		{"", []string{"production", "list"}},
		{"", []string{"production", "list", "p", "t", "u"}},
		{"", []string{"production", "list", "../p"}},
		{`[{"path":"p/a","revision":"","value":1}]`, []string{"production", "cas", "p/a"}},
		{`[{"path":"p/a","revision":"","value":1},{"path":"p/b","revision":"","value":1},{"path":"p/c","revision":"","value":1}]`, []string{"production", "cas"}},
		{`[]`, []string{"production", "cas"}},
		{`[{"path":"p/a","revision":"","value":1}`, []string{"production", "cas"}},
		{`[{"path":"p/a","revision":"","value":1,"extra":true}]`, []string{"production", "cas"}},
		{`[{"path":"p/a","revision":"","value":1}] []`, []string{"production", "cas"}},
		{`[{"path":"p/a","revision":""}]`, []string{"production", "cas"}},
	} {
		if got := kv(t, root, call.stdin, call.argv...); got.code != 2 || got.stdout != "" || !strings.HasPrefix(got.stderr, "ocel-live kv: ") {
			t.Errorf("kv %q with %q = %+v, want exit 2 with the reason on stderr", call.argv, call.stdin, got)
		}
	}
	if got := kv(t, root, "", "production", "list", "p"); got.code != 0 || got.stdout != "" {
		t.Errorf("a refused cas left entries behind: %+v", got)
	}
}
