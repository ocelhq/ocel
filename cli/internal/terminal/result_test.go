package terminal

import (
	"bytes"
	"testing"

	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func TestAResultUnderJSONIsOneCompactLineHoldingTheEnvelope(t *testing.T) {
	var out bytes.Buffer
	if err := WriteResultJSON(&out, &resultv1.BindingRemoveResult{Name: "main"}); err != nil {
		t.Fatal(err)
	}

	want := `{"ok":true,"data":{"name":"main","removed":false}}` + "\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestAResultKeepsListsAndFlagsHoldingTheirZeroValue(t *testing.T) {
	var out bytes.Buffer
	if err := WriteResultJSON(&out, &resultv1.DomainStatusResult{}); err != nil {
		t.Fatal(err)
	}

	want := `{"ok":true,"data":{"ready":false,"recordsWritten":[],"manualRecords":[],"hosts":[]}}` + "\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestAResultOmitsAnUnsetMessageInsteadOfPrintingNull(t *testing.T) {
	var out bytes.Buffer
	if err := WriteResultJSON(&out, &resultv1.PreviewDomainResult{BaseDomain: "preview.example.com"}); err != nil {
		t.Fatal(err)
	}

	want := `{"ok":true,"data":{"baseDomain":"preview.example.com","edgeScope":"","routeInstalled":false,"projects":[]}}` + "\n"
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
