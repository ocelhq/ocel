package cloudlink

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const apiURL = "https://ocel.app"

func sample() Link {
	return Link{
		APIURL:         apiURL,
		OrganizationID: "org_1",
		ProjectID:      "proj_1",
		ProjectName:    "My App",
	}
}

func TestReadNoRecordReportsUnlinked(t *testing.T) {
	link, err := Read(t.TempDir(), apiURL)
	if err != nil {
		t.Fatalf("Read err = %v, want nil", err)
	}
	if link != nil {
		t.Fatalf("Read = %+v, want nil", link)
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample()); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	link, err := Read(dir, apiURL)
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if link == nil {
		t.Fatal("Read = nil, want the record just written")
	}
	if *link != sample() {
		t.Fatalf("Read = %+v, want %+v", *link, sample())
	}
}

func TestWriteStoresRecordInScratchDir(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample()); err != nil {
		t.Fatalf("Write err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ocel", "link.json")); err != nil {
		t.Fatalf("stat .ocel/link.json: %v", err)
	}
}

func TestReadDifferentAPIURLReportsUnlinked(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample()); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	link, err := Read(dir, "http://localhost:3000")
	if err != nil {
		t.Fatalf("Read err = %v, want nil", err)
	}
	if link != nil {
		t.Fatalf("Read = %+v, want nil for a record from another control plane", link)
	}
}

func TestReadIgnoresTrailingSlashDifference(t *testing.T) {
	dir := t.TempDir()
	link := sample()
	link.APIURL = apiURL + "/"
	if err := Write(dir, link); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	got, err := Read(dir, apiURL)
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if got == nil {
		t.Fatal("Read = nil, want the record (a trailing slash is the same origin)")
	}
	if got.APIURL != apiURL {
		t.Fatalf("APIURL = %q, want it normalized to %q", got.APIURL, apiURL)
	}
}

func TestReadMalformedRecordErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ocel"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ocel", "link.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Read(dir, apiURL); err == nil {
		t.Fatal("Read err = nil, want an error for a malformed record")
	} else if !strings.Contains(err.Error(), "ocel unlink") {
		t.Fatalf("err = %v, want it to suggest `ocel unlink`", err)
	}
}

func TestWriteReplacesAnExistingRecord(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample()); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	replacement := Link{APIURL: apiURL, OrganizationID: "org_2", ProjectID: "proj_2", ProjectName: "Other"}
	if err := Write(dir, replacement); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	link, err := Read(dir, apiURL)
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if link == nil || *link != replacement {
		t.Fatalf("Read = %+v, want %+v", link, replacement)
	}
}

func TestClearRemovesTheRecord(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, sample()); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	removed, err := Clear(dir)
	if err != nil {
		t.Fatalf("Clear err = %v", err)
	}
	if !removed {
		t.Fatal("Clear removed = false, want true")
	}

	link, err := Read(dir, apiURL)
	if err != nil {
		t.Fatalf("Read err = %v", err)
	}
	if link != nil {
		t.Fatalf("Read = %+v after Clear, want nil", link)
	}
}

func TestClearWithNothingToRemoveIsNotAnError(t *testing.T) {
	removed, err := Clear(t.TempDir())
	if err != nil {
		t.Fatalf("Clear err = %v, want nil", err)
	}
	if removed {
		t.Fatal("Clear removed = true, want false")
	}
}

func TestClearRemovesARecordFromAnotherControlPlane(t *testing.T) {
	dir := t.TempDir()
	link := sample()
	link.APIURL = "http://localhost:3000"
	if err := Write(dir, link); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	removed, err := Clear(dir)
	if err != nil {
		t.Fatalf("Clear err = %v", err)
	}
	if !removed {
		t.Fatal("Clear removed = false, want true — unlink is not scoped to one control plane")
	}
}
