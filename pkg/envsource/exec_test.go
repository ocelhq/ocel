package envsource_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestAnExecEnvSourceSpansItsCommandWithoutWhatItPrinted(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "values.json"), []byte(`{"API_KEY":"sk-live-4e1d"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "warning.txt"), []byte("token tok-b72a expires soon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := execDescriptor(envsource.FormatJSON, "sh", "-c", "cat values.json; cat warning.txt >&2").Open(dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Read(ctx, []string{""}); err != nil {
		t.Fatalf("Read() = %v", err)
	}

	span := recorded.Only(t)
	attrs := tracingtest.Attributes(span)
	if args := strings.Join(attrs["process.command_args"].AsStringSlice(), " "); span.Name() != "sh" || args != "sh -c cat values.json; cat warning.txt >&2" {
		t.Errorf("span %q ran %q, want sh named with its arguments", span.Name(), args)
	}
	if exit, recordedExit := attrs["process.exit.code"]; !recordedExit || exit.AsInt64() != 0 {
		t.Errorf("process.exit.code = %v (recorded %t), want 0", exit, recordedExit)
	}
	exported := tracingtest.ExportedText(span)
	for _, printed := range []string{"sk-live-4e1d", "tok-b72a"} {
		if strings.Contains(exported, printed) {
			t.Errorf("span exports\n%s\nwant nothing the command printed, where %q is", exported, printed)
		}
	}
}
