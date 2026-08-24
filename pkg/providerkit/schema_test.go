package providerkit_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func TestTheRootSchemaIsWrittenOnceAndReadBack(t *testing.T) {
	records := fake.NewRecords()
	ctx := context.Background()

	if written, err := providerkit.RecordSchema(ctx, records, providerkit.ClassProduction); err != nil || written != 0 {
		t.Fatalf("RecordSchema() of an unwritten tree = %d, %v, want 0", written, err)
	}
	if err := providerkit.EnsureRecordSchema(ctx, records, providerkit.ClassProduction); err != nil {
		t.Fatal(err)
	}
	held, err := records.Read(ctx, providerkit.SchemaRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := providerkit.EnsureRecordSchema(ctx, records, providerkit.ClassProduction); err != nil {
		t.Fatalf("a second EnsureRecordSchema() = %v, want it a no-op", err)
	}
	again, err := records.Read(ctx, providerkit.SchemaRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != held.Revision {
		t.Fatalf("the schema record was rewritten at revision %q, want the %q already written", again.Revision, held.Revision)
	}
	if written, err := providerkit.RecordSchema(ctx, records, providerkit.ClassProduction); err != nil || written != providerkit.RecordSchemaVersion {
		t.Fatalf("RecordSchema() = %d, %v, want %d", written, err, providerkit.RecordSchemaVersion)
	}
}

func TestARecordTreeANewerOcelWroteIsRefused(t *testing.T) {
	records := fake.NewRecords()
	ctx := context.Background()

	ahead := strconv.Itoa(providerkit.RecordSchemaVersion + 1)
	if _, err := records.Write(ctx, providerkit.Record{Name: providerkit.SchemaRecord(providerkit.ClassProduction), Bytes: []byte(ahead)}); err != nil {
		t.Fatal(err)
	}

	err := providerkit.EnsureRecordSchema(ctx, records, providerkit.ClassProduction)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("EnsureRecordSchema() over a newer tree = %v, want it refused as not ready", err)
	}
	held, err := records.Read(ctx, providerkit.SchemaRecord(providerkit.ClassProduction))
	if err != nil || string(held.Bytes) != ahead {
		t.Fatalf("the refused downgrade left %q behind, want the tree untouched at %q", held.Bytes, ahead)
	}
}
