package projectinit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var errWriteFailed = errors.New("write failed")

func failingWrite(*os.File, []byte) error { return errWriteFailed }

func closingWrite(file *os.File, _ []byte) error { return file.Close() }

func closingFailingWrite(file *os.File, _ []byte) error {
	_ = file.Close()
	return errWriteFailed
}

func unsupportedLink(string, string) error {
	return &os.LinkError{Op: "link", Err: errors.ErrUnsupported}
}

func entriesOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestNewFileWritesTheContentAndLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]newFile{
		"linked":                    systemNewFile,
		"without hard link support": {write: writeAll, link: unsupportedLink},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "ocel.json")

			if err := file.create(path, []byte("{}\n")); err != nil {
				t.Fatalf("create = %v", err)
			}

			if got, _ := os.ReadFile(path); string(got) != "{}\n" {
				t.Errorf("content = %q, want %q", got, "{}\n")
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
				t.Errorf("stat = %v, %v, want mode 0644", info, err)
			}
			if got := entriesOf(t, dir); !slices.Equal(got, []string{"ocel.json"}) {
				t.Errorf("directory holds %q, want only ocel.json", got)
			}
		})
	}
}

func TestNewFileLeavesAnExistingFileAsItWasAndReportsItExists(t *testing.T) {
	t.Parallel()
	for name, file := range map[string]newFile{
		"linked":                    systemNewFile,
		"without hard link support": {write: writeAll, link: unsupportedLink},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "ocel.json")
			if err := os.WriteFile(path, []byte("theirs\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := file.create(path, []byte("ours\n")); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("create = %v, want fs.ErrExist", err)
			}

			if got, _ := os.ReadFile(path); string(got) != "theirs\n" {
				t.Errorf("content = %q, want the existing file left as it was", got)
			}
			if got := entriesOf(t, dir); !slices.Equal(got, []string{"ocel.json"}) {
				t.Errorf("directory holds %q, want only ocel.json", got)
			}
		})
	}
}

func TestNewFileThatFailsToWriteLeavesNothingBehindAndReturnsTheWriteError(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		file newFile
		want error
	}{
		"write fails":                    {newFile{write: failingWrite, link: os.Link}, errWriteFailed},
		"close fails":                    {newFile{write: closingWrite, link: os.Link}, os.ErrClosed},
		"write and close fail":           {newFile{write: closingFailingWrite, link: os.Link}, errWriteFailed},
		"write fails without hard links": {newFile{write: failingWrite, link: unsupportedLink}, errWriteFailed},
		"close fails without hard links": {newFile{write: closingWrite, link: unsupportedLink}, os.ErrClosed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()

			if err := test.file.create(filepath.Join(dir, "ocel.json"), []byte("{}\n")); !errors.Is(err, test.want) {
				t.Fatalf("create = %v, want %v", err, test.want)
			}

			if got := entriesOf(t, dir); len(got) > 0 {
				t.Errorf("directory holds %q, want nothing so a retry is not init_config_exists", got)
			}
		})
	}
}
