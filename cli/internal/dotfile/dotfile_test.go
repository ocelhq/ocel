package dotfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnAbsentFileLoadsAsEmpty(t *testing.T) {
	t.Parallel()
	file, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(file.Values) != 0 {
		t.Fatalf("Load = %v, want empty", file.Values)
	}
}

func TestLoadReadsTheFileInTheDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("DATABASE_URL=postgres://localhost/app\nnot an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if file.Values["DATABASE_URL"] != "postgres://localhost/app" || len(file.Unreadable) != 1 || file.Unreadable[0] != 2 {
		t.Fatalf("Load = %+v, want the value read and line 2 reported unreadable", file)
	}
}

func TestLoadLocalReadsTheDevelopersOwnFileAndNotTheSharedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("SHARED=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LocalFileName), []byte("MINE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := LoadLocal(dir)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if file.Values["MINE"] != "yes" || len(file.Values) != 1 {
		t.Fatalf("LoadLocal = %v, want only %s's value", file.Values, LocalFileName)
	}
	if absent, err := LoadLocal(t.TempDir()); err != nil || len(absent.Values) != 0 {
		t.Fatalf("LoadLocal of a directory without %s = %v, %v, want empty", LocalFileName, absent.Values, err)
	}
}
