package pulumi_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/costsource/pulumi"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name string, msg proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := json.Indent(&got, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	got.WriteByte('\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, got.Bytes()) {
		t.Errorf("%s differs from the golden file; run with -update after checking the diff:\n%s", name, got.String())
	}
}

func TestTheParsedInventoryMatchesTheGoldenFiles(t *testing.T) {
	golden(t, "preview", parse(t, "preview.json", pulumi.Options{Source: pulumi.SourcePulumi, Name: "dev"}))
	golden(t, "sst_state", parse(t, "sst_state.json", pulumi.Options{Source: pulumi.SourceSST, Name: "victor"}))

	merged, err := pulumi.Merge(fixture(t, "sst_state.json"), fixture(t, "sst_diff.json"), pulumi.Options{Source: pulumi.SourceSST, Name: "victor"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "sst", merged)
}
