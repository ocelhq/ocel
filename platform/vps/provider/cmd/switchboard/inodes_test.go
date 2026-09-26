package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInodesNamesTheDeviceAndInodeOfEachPathItIsHanded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	table := filepath.Join(dir, "table.json")
	if err := os.WriteFile(table, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, path := range []string{dir, table} {
		var info syscall.Stat_t
		if err := syscall.Stat(path, &info); err != nil {
			t.Fatal(err)
		}
		want = append(want, fmt.Sprintf("%s %d:%d", path, info.Dev, info.Ino))
	}

	code, out, errs := ran(t, "inodes", dir, table)
	if code != 0 {
		t.Fatalf("inodes = %d: %s", code, errs)
	}
	if got := strings.Split(strings.TrimSpace(out), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("inodes printed %q, want %q: the host compares each line with `stat -c %%d:%%i` of the bind's source, and a line in any other shape reads as a mount that moved", got, want)
	}
}

func TestInodesRefusesAPathItCannotStatAndNamesIt(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), "gone")
	code, out, errs := ran(t, "inodes", gone)
	if code != exitRefused || out != "" {
		t.Errorf("inodes of a path that is not there = %d, %q, want the refusal and nothing printed: a partial answer reads as a mount the probe checked", code, out)
	}
	if !strings.Contains(errs, gone) {
		t.Errorf("inodes refused with %q, which never names %s", errs, gone)
	}
	if code, _, _ := ran(t, "inodes"); code != exitRefused {
		t.Errorf("inodes of nothing = %d, want the usage refusal", code)
	}
}
