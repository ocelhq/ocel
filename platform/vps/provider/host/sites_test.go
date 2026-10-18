package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func sites(t *testing.T, marker string) []string {
	t.Helper()
	var entries []string
	for _, dir := range []string{".", "../docker", "../routing", "../tunnel", "../release", "../envfile", "../resources/container", "../front", "../machine"} {
		matched, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range matched {
			if !strings.HasSuffix(entry, "_test.go") {
				entries = append(entries, entry)
			}
		}
	}
	var found []string
	for _, entry := range entries {
		read, err := os.ReadFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		enclosing := ""
		for _, line := range strings.Split(string(read), "\n") {
			if name, cut := functionName(line); cut {
				enclosing = name
			}
			if strings.Contains(line, marker) && enclosing != "" && !slices.Contains(found, enclosing) {
				found = append(found, enclosing)
			}
		}
	}
	slices.Sort(found)
	return found
}

func functionName(line string) (string, bool) {
	if !strings.HasPrefix(line, "func ") {
		return "", false
	}
	signature := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(signature, "(") {
		_, signature, _ = strings.Cut(signature, ") ")
	}
	name, _, cut := strings.Cut(signature, "(")
	return name, cut
}

func rendered(t *testing.T, marker string, roster []string, why string) {
	t.Helper()
	found := sites(t, marker)
	if len(found) == 0 {
		t.Fatalf("no source this bench reads renders %s, so the roster this bench checks against it proves nothing", marker)
	}
	slices.Sort(roster)
	if !slices.Equal(found, roster) {
		t.Errorf("%s is rendered by %v and this bench reads %v: %s", marker, found, roster, why)
	}
}
