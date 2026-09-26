package host

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestWhatTheSealHelperSealsTheBoxOpensNativelyAtTheSameCoordinate(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	sealed, code := sealHelperAt(t, root, encoded("postgres://example"), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		t.Fatal(err)
	}
	if got := live.KeyPath(root, bound.Class); got != filepath.Join(root, sealClass, "seal.key") {
		t.Fatalf("the agent reads the key at %s, and the helper minted it at %s", got, filepath.Join(root, sealClass, "seal.key"))
	}

	vault := live.Cipher{Root: root}
	opened, err := vault.Open(context.Background(), bound, raw)
	if err != nil {
		t.Fatalf("Open() of what the helper sealed = %v", err)
	}
	if string(opened) != "postgres://example" {
		t.Errorf("Open() = %q, want what was sealed", opened)
	}
	moved := bound
	moved.Env = "staging"
	if _, err := vault.Open(context.Background(), moved, raw); err == nil {
		t.Error("a value the helper sealed for one environment opened natively for another")
	}
}

func TestWhatTheRecordsHelperWritesTheBoxReadsNatively(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	names := []string{
		"values/shop/production/cells/%2F/DATABASE_URL/%2A",
		"values/shop/production/cells/%2F/SESSION/pr-7",
	}
	for _, name := range names {
		helperWrite(t, dir, name, "", "body of "+name)
	}
	store := live.Records{Root: dir}
	record, err := store.Read(context.Background(), records.Name{"values", "shop", "production", "cells", "/", "DATABASE_URL", "*"})
	if err != nil {
		t.Fatalf("Read() of what the helper wrote = %v", err)
	}
	if string(record.Bytes) != "body of "+names[0] {
		t.Errorf("Read() = %q, want what the helper wrote", record.Bytes)
	}
	if revision, _ := helperRead(t, dir, names[0]); string(record.Revision) != revision {
		t.Errorf("Read() returns revision %q, and the helper says %q", record.Revision, revision)
	}
	listed, err := store.List(context.Background(), records.Name{"values", "shop", "production", "cells"})
	if err != nil || len(listed) != 2 {
		t.Fatalf("List() = %v, %v, want the two cells the helper wrote", listed, err)
	}
	if dir := live.RecordsDir(dir, helperClass); !strings.HasSuffix(dir, filepath.Join(helperClass, "records")) {
		t.Errorf("the agent reads records under %s, and the helper keeps them under <root>/<class>/records", dir)
	}
}
