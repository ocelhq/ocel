package providerserver

import (
	"fmt"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestAFunctionSpecSaysWhetherItsArtifactCarriesTheWorkerEntry(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{Apps: []*contractv1.ManifestApp{{
		Name: "web",
		Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: []*contractv1.ManifestFunction{
			{LogicalName: "fn--web--a"},
			{LogicalName: "fn--web--b", HasWorkerEntry: true},
		}}},
	}}}

	specs := inventoryFunctions(manifest)["web"]
	if len(specs) != 2 || specs[0].HasWorkerEntry || !specs[1].HasWorkerEntry {
		t.Errorf("inventoried functions = %+v, want only fn--web--b to carry the worker entry", specs)
	}
}

func TestAFunctionWhoseBuildPathLeavesTheBuildRootIsRefusedWhereItIsRead(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"/srv/web/server.func", "../server.func", "apps/../../server.func"} {
		if _, err := artifactDir(root, &contractv1.ManifestFunction{LogicalName: "server", ArtifactPath: path}); !strings.Contains(fmt.Sprint(err), "leaves the build root") {
			t.Errorf("artifactDir(%q) = %v, want a refusal: a function is read from inside the build root alone", path, err)
		}
	}
}
