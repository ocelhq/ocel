package providerserver

import (
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

	specs := shapedFunctions(manifest)["web"]
	if len(specs) != 2 || specs[0].HasWorkerEntry || !specs[1].HasWorkerEntry {
		t.Errorf("shaped functions = %+v, want only fn--web--b to carry the worker entry", specs)
	}
}
