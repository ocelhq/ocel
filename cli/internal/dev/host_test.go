package dev

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestTwoConfigsInOneDirectoryKeepTheirOwnDevResources(t *testing.T) {
	root := t.TempDir()
	aws := &project.Project{Dir: root, Path: filepath.Join(root, "ocel.json")}
	vps := &project.Project{Dir: root, Path: filepath.Join(root, "ocel.vps.json")}

	if leaderKey(aws) == leaderKey(vps) {
		t.Fatalf("leader key = %q for both configs, want one leader per config", leaderKey(aws))
	}
	if resourcesName(aws) == resourcesName(vps) {
		t.Errorf("dev resources name = %q for both configs, want each config's leader to own its own queue and database", resourcesName(aws))
	}
	if stateDir(aws) == stateDir(vps) {
		t.Errorf("dev resources state dir = %q for both configs, want each leader to keep its own", stateDir(aws))
	}
}

func TestTheAppsOriginsFollowThePortInTheDotfile(t *testing.T) {
	root := t.TempDir()
	origins := appOrigins(root, valueSource{id: "dotenv", ownStore: true})

	clitest.WriteFile(t, filepath.Join(root, project.DotenvFileName), "PORT=4100\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4100") || !slices.Contains(got, "http://127.0.0.1:4100") {
		t.Fatalf("origins = %v, want the app on port 4100", got)
	}
	clitest.WriteFile(t, filepath.Join(root, project.DotenvFileName), "PORT=4200\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4200") || slices.Contains(got, "http://localhost:4100") {
		t.Fatalf("origins = %v after the port moved to 4200", got)
	}
}
