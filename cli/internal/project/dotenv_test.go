package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnAbsentFileLoadsAsEmpty(t *testing.T) {
	t.Parallel()
	file, err := ReadDotenv(t.TempDir())
	if err != nil {
		t.Fatalf("ReadDotenv: %v", err)
	}
	if len(file.Values) != 0 {
		t.Fatalf("ReadDotenv = %v, want empty", file.Values)
	}
}

func TestTheDotenvIsReadFromTheDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotenvFileName), []byte("DATABASE_URL=postgres://localhost/app\nnot an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := ReadDotenv(dir)
	if err != nil {
		t.Fatalf("ReadDotenv: %v", err)
	}
	if file.Values["DATABASE_URL"] != "postgres://localhost/app" || len(file.Unreadable) != 1 || file.Unreadable[0] != 2 {
		t.Fatalf("ReadDotenv = %+v, want the value read and line 2 reported unreadable", file)
	}
}

func TestTheLocalDotenvIsTheDevelopersOwnFileAndNotTheSharedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DotenvFileName), []byte("SHARED=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LocalDotenvFileName), []byte("MINE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := ReadLocalDotenv(dir)
	if err != nil {
		t.Fatalf("ReadLocalDotenv: %v", err)
	}
	if file.Values["MINE"] != "yes" || len(file.Values) != 1 {
		t.Fatalf("ReadLocalDotenv = %v, want only %s's value", file.Values, LocalDotenvFileName)
	}
	if absent, err := ReadLocalDotenv(t.TempDir()); err != nil || len(absent.Values) != 0 {
		t.Fatalf("ReadLocalDotenv of a directory without %s = %v, %v, want empty", LocalDotenvFileName, absent.Values, err)
	}
}
