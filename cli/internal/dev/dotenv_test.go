package dev

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestAnAbsentFileLoadsAsEmpty(t *testing.T) {
	t.Parallel()
	file, err := readDotenv(t.TempDir())
	if err != nil {
		t.Fatalf("readDotenv: %v", err)
	}
	if len(file.Values) != 0 {
		t.Fatalf("readDotenv = %v, want empty", file.Values)
	}
}

func TestTheDotenvIsReadFromTheDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.DotenvFileName), []byte("DATABASE_URL=postgres://localhost/app\nnot an assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := readDotenv(dir)
	if err != nil {
		t.Fatalf("readDotenv: %v", err)
	}
	if file.Values["DATABASE_URL"] != "postgres://localhost/app" || len(file.Unreadable) != 1 || file.Unreadable[0] != 2 {
		t.Fatalf("readDotenv = %+v, want the value read and line 2 reported unreadable", file)
	}
}

func TestTheLocalDotenvIsTheDevelopersOwnFileAndNotTheSharedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.DotenvFileName), []byte("SHARED=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, project.LocalDotenvFileName), []byte("MINE=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := readLocalDotenv(dir)
	if err != nil {
		t.Fatalf("readLocalDotenv: %v", err)
	}
	if file.Values["MINE"] != "yes" || len(file.Values) != 1 {
		t.Fatalf("readLocalDotenv = %v, want only %s's value", file.Values, project.LocalDotenvFileName)
	}
	if absent, err := readLocalDotenv(t.TempDir()); err != nil || len(absent.Values) != 0 {
		t.Fatalf("readLocalDotenv of a directory without %s = %v, %v, want empty", project.LocalDotenvFileName, absent.Values, err)
	}
}
