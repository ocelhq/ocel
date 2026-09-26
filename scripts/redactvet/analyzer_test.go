package main

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestFormattingAMessageWithARedactedFieldIsReported(t *testing.T) {
	analysistest.Run(t, ".", Analyzer, "./testdata/leak")
}

func TestAGeneratedPackageNamesItsRedactingMessagesByTheirGoTypes(t *testing.T) {
	analysistest.Run(t, ".", Analyzer, "./testdata/named")
}

func TestARedactingMessageWithNoGoTypeUnderItsNameIsReported(t *testing.T) {
	analysistest.Run(t, ".", Analyzer, "./testdata/drift")
}

func TestTheGeneratedStringOfADeployRequestStillPrintsItsRegistryPassword(t *testing.T) {
	var req fmt.Stringer = &contractv1.DeployRequest{ImageRegistry: &contractv1.ImageRegistry{Server: "ghcr.io", Password: "ghp_livesecret"}}
	if !strings.Contains(req.String(), "ghp_livesecret") {
		t.Fatal("DeployRequest.String() redacts its registry password, so protobuf-go honours debug_redact now: retire redactvet and bump every module to that release")
	}
}
