package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/statedir"
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

func TestReadLink(t *testing.T) {
	t.Parallel()

	fromAnotherControlPlane := sample()
	fromAnotherControlPlane.APIURL = "http://localhost:3000"

	unlinked := []struct {
		name   string
		stored *Link
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
				if err := WriteLink(dir, *tt.stored); err != nil {
					t.Fatalf("WriteLink err = %v", err)
				}
			}

			link, err := ReadLink(dir, apiURL)
			if err != nil {
				t.Fatalf("ReadLink err = %v, want nil", err)
			}
			if link != nil {
				t.Fatalf("ReadLink = %+v, %s", link, tt.reason)
			}
		})
	}

	t.Run("ignores a trailing slash difference", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		link := sample()
		link.APIURL = apiURL + "/"
		if err := WriteLink(dir, link); err != nil {
			t.Fatalf("WriteLink err = %v", err)
		}

		got, err := ReadLink(dir, apiURL)
		if err != nil {
			t.Fatalf("ReadLink err = %v", err)
		}
		if got == nil {
			t.Fatal("ReadLink = nil, want the record (a trailing slash is the same origin)")
		}
		if got.APIURL != apiURL {
			t.Fatalf("APIURL = %q, want it normalized to %q", got.APIURL, apiURL)
		}
	})

	t.Run("a malformed record errors", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, statedir.Name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, statedir.Name, "console.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, err := ReadLink(dir, apiURL); err == nil {
			t.Fatal("ReadLink err = nil, want an error for a malformed record")
		} else if !strings.Contains(err.Error(), "ocel unlink") {
			t.Fatalf("err = %v, want it to suggest `ocel unlink`", err)
		}
	})
}

func TestWriteLink(t *testing.T) {
	t.Parallel()

	t.Run("then read round trips", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := WriteLink(dir, sample()); err != nil {
			t.Fatalf("WriteLink err = %v", err)
		}

		link, err := ReadLink(dir, apiURL)
		if err != nil {
			t.Fatalf("ReadLink err = %v", err)
		}
		if link == nil {
			t.Fatal("ReadLink = nil, want the record just written")
		}
		if *link != sample() {
			t.Fatalf("ReadLink = %+v, want %+v", *link, sample())
		}
	})

	t.Run("stores the record in the scratch dir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := WriteLink(dir, sample()); err != nil {
			t.Fatalf("WriteLink err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, statedir.Name, "console.json")); err != nil {
			t.Fatalf("stat %s/console.json: %v", statedir.Name, err)
		}
	})

	t.Run("replaces an existing record", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		if err := WriteLink(dir, sample()); err != nil {
			t.Fatalf("WriteLink err = %v", err)
		}

		replacement := Link{APIURL: apiURL, OrganizationID: "org_2", ProjectID: "proj_2", ProjectName: "Other"}
		if err := WriteLink(dir, replacement); err != nil {
			t.Fatalf("WriteLink err = %v", err)
		}

		link, err := ReadLink(dir, apiURL)
		if err != nil {
			t.Fatalf("ReadLink err = %v", err)
		}
		if link == nil || *link != replacement {
			t.Fatalf("ReadLink = %+v, want %+v", link, replacement)
		}
	})
}

func TestDeleteLink(t *testing.T) {
	t.Parallel()

	bound := sample()
	fromAnotherControlPlane := sample()
	fromAnotherControlPlane.APIURL = "http://localhost:3000"

	tests := []struct {
		name        string
		stored      *Link
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
				if err := WriteLink(dir, *tt.stored); err != nil {
					t.Fatalf("WriteLink err = %v", err)
				}
			}

			removed, err := DeleteLink(dir)
			if err != nil {
				t.Fatalf("DeleteLink err = %v, want nil", err)
			}
			if removed != tt.wantRemoved {
				t.Fatalf("DeleteLink removed = %v, %s", removed, tt.reason)
			}

			link, err := ReadLink(dir, apiURL)
			if err != nil {
				t.Fatalf("ReadLink err = %v", err)
			}
			if link != nil {
				t.Fatalf("ReadLink = %+v after DeleteLink, want nil", link)
			}
		})
	}
}
