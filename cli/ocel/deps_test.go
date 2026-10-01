package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTheCLINeverLinksTheProviderServer(t *testing.T) {
	t.Parallel()

	const server = "github.com/ocelhq/ocel/pkg/provider/providerserver"
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if pkg == server || strings.HasPrefix(pkg, server+"/") {
			t.Errorf("the ocel binary links %s: the server runs inside a provider binary, and the CLI only speaks to it over the wire", pkg)
		}
	}
}

func TestTheCLIReachesPostgresOnlyThroughTheDevQueueEngine(t *testing.T) {
	t.Parallel()

	const (
		driver = "github.com/jackc/pgx"
		engine = "github.com/ocelhq/ocel/platform/pgmq"
	)
	out, err := exec.Command("go", "list", "-deps", "-f", `{{.ImportPath}} {{join .Imports " "}}`, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, " ")
		if strings.HasPrefix(pkg, driver) || pkg == engine || strings.HasPrefix(pkg, engine+"/") {
			continue
		}
		for _, imported := range strings.Fields(imports) {
			if strings.HasPrefix(imported, driver) {
				t.Errorf("%s imports %s: the provider checks the database an inline binding names, inside the deploy, and the only database the CLI opens is ocel dev's own queue database", pkg, imported)
			}
		}
	}
}
