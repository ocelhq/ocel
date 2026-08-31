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
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}
		read, err := os.ReadFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		holding := ""
		for _, line := range strings.Split(string(read), "\n") {
			if name, cut := functionName(line); cut {
				holding = name
			}
			if strings.Contains(line, marker) && holding != "" && !slices.Contains(found, holding) {
				found = append(found, holding)
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
	held := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(held, "(") {
		_, held, _ = strings.Cut(held, ") ")
	}
	name, _, cut := strings.Cut(held, "(")
	return name, cut
}

func rendered(t *testing.T, marker string, roster []string, why string) {
	t.Helper()
	found := sites(t, marker)
	if len(found) == 0 {
		t.Fatalf("no source in this package renders %s, so the roster this bench holds to it proves nothing", marker)
	}
	slices.Sort(roster)
	if !slices.Equal(found, roster) {
		t.Errorf("%s is rendered by %v and this bench reads %v: %s", marker, found, roster, why)
	}
}
