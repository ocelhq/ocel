package provider_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTheRecordsAHumanMustWriteForACertificateAreStoredUnderTheirLiteralName(t *testing.T) {
	t.Parallel()

	stored, err := json.Marshal(provider.Certificate{Manual: []edge.Record{{Name: "_acme.shop.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), `"manual":`) {
		t.Errorf("a certificate is stored as %s, want the manual records under %q", stored, "manual")
	}
}
