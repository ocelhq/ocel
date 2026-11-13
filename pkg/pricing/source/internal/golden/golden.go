package golden

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func Match(t *testing.T, name string, set, prior *costv1.ResourceSet) {
	t.Helper()
	sets := map[string]any{"set": encode(t, set)}
	if prior != nil {
		sets["prior"] = encode(t, prior)
	}
	got, err := json.MarshalIndent(sets, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s differs from %s; run with -update after checking the diff:\n%s", name, path, got)
	}
}

func encode(t *testing.T, set *costv1.ResourceSet) any {
	t.Helper()
	if err := protovalidate.Validate(set); err != nil {
		t.Errorf("the resource set is invalid: %v", err)
	}
	raw, err := protojson.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
