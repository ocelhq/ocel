package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAFunctionCarriesWhatTheDeployDeliveredAndWhatItsSpecNames(t *testing.T) {
	values, err := carried("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"STAGE": "one"})
	if err != nil {
		t.Fatalf("carried() = %v", err)
	}
	if values["DATABASE_URL"] != "postgres://" || values["STAGE"] != "one" {
		t.Errorf("carried() = %v, want both what the deploy resolved and what the spec named", values)
	}
}

func TestASpecThatNamesWhatTheDeployDeliveredIsRefused(t *testing.T) {
	_, err := carried("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"DATABASE_URL": "sqlite://"})
	if err == nil {
		t.Fatal("carried() let the spec take the delivered value's place, and the app would read a value nothing in it declared")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("carried() code = %v, want %v", code, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("carried() = %v, want the name that would be displaced said", err)
	}
	if !strings.Contains(err.Error(), "fn") {
		t.Errorf("carried() = %v, want what carries it said", err)
	}
}
