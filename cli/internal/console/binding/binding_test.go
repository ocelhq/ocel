package binding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const apiURL = "https://ocel.app"

func sample() Binding {
	return Binding{
		APIURL:         apiURL,
		OrganizationID: "org_1",
		ProjectID:      "proj_1",
		ProjectName:    "My App",
	}
}

func TestRead(t *testing.T) {
	t.Parallel()

	fromAnotherControlPlane := sample()
	fromAnotherControlPlane.APIURL = "http://localhost:3000"

	unlinked := []struct {
		name   string
		stored *Binding
		reason string
	}{
		{
			name:   "no record reports unlinked",
			stored: nil,
			reason: "want nil",
		},
		{
			name:   "a record from a different API URL reports unlinked",
			stored: &fromAnotherControlPlane,
			reason: "want nil for a record from another control plane",
		},
	}
	for _, tt := range unlinked {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.stored != nil {
				if err := Write(dir, *tt.stored); err != nil {
					t.Fatalf("Write err = %v", err)
				}
			}

			binding, err := Read(dir, apiURL)
			if err != nil {
				t.Fatalf("Read err = %v, want nil", err)
			}
			if binding != nil {
				t.Fatalf("Read = %+v, %s", binding, tt.reason)
			}
		})
	}

	t.Run("ignores a trailing slash difference", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		binding := sample()
		binding.APIURL = apiURL + "/"
		if err := Write(dir, binding); err != nil {
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
	})

	t.Run("a malformed record errors", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".ocel"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".ocel", "console.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, err := Read(dir, apiURL); err == nil {
			t.Fatal("Read err = nil, want an error for a malformed record")
		} else if !strings.Contains(err.Error(), "ocel console unlink") {
			t.Fatalf("err = %v, want it to suggest `ocel console unlink`", err)
		}
	})
}

func TestWrite(t *testing.T) {
	t.Parallel()

	t.Run("then read round trips", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := Write(dir, sample()); err != nil {
			t.Fatalf("Write err = %v", err)
		}

		binding, err := Read(dir, apiURL)
		if err != nil {
			t.Fatalf("Read err = %v", err)
		}
		if binding == nil {
			t.Fatal("Read = nil, want the record just written")
		}
		if *binding != sample() {
			t.Fatalf("Read = %+v, want %+v", *binding, sample())
		}
	})

	t.Run("stores the record in the scratch dir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := Write(dir, sample()); err != nil {
			t.Fatalf("Write err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".ocel", "console.json")); err != nil {
			t.Fatalf("stat .ocel/console.json: %v", err)
		}
	})

	t.Run("replaces an existing record", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := Write(dir, sample()); err != nil {
			t.Fatalf("Write err = %v", err)
		}

		replacement := Binding{APIURL: apiURL, OrganizationID: "org_2", ProjectID: "proj_2", ProjectName: "Other"}
		if err := Write(dir, replacement); err != nil {
			t.Fatalf("Write err = %v", err)
		}

		binding, err := Read(dir, apiURL)
		if err != nil {
			t.Fatalf("Read err = %v", err)
		}
		if binding == nil || *binding != replacement {
			t.Fatalf("Read = %+v, want %+v", binding, replacement)
		}
	})
}

func TestClear(t *testing.T) {
	t.Parallel()

	bound := sample()
	fromAnotherControlPlane := sample()
	fromAnotherControlPlane.APIURL = "http://localhost:3000"

	tests := []struct {
		name        string
		stored      *Binding
		wantRemoved bool
		reason      string
	}{
		{
			name:        "removes the record",
			stored:      &bound,
			wantRemoved: true,
			reason:      "want true",
		},
		{
			name:        "with nothing to remove is not an error",
			stored:      nil,
			wantRemoved: false,
			reason:      "want false",
		},
		{
			name:        "removes a record from another control plane",
			stored:      &fromAnotherControlPlane,
			wantRemoved: true,
			reason:      "want true — unlink is not scoped to one control plane",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.stored != nil {
				if err := Write(dir, *tt.stored); err != nil {
					t.Fatalf("Write err = %v", err)
				}
			}

			removed, err := Clear(dir)
			if err != nil {
				t.Fatalf("Clear err = %v, want nil", err)
			}
			if removed != tt.wantRemoved {
				t.Fatalf("Clear removed = %v, %s", removed, tt.reason)
			}

			binding, err := Read(dir, apiURL)
			if err != nil {
				t.Fatalf("Read err = %v", err)
			}
			if binding != nil {
				t.Fatalf("Read = %+v after Clear, want nil", binding)
			}
		})
	}
}
