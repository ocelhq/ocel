package variables_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestOnlyADeclaredKeyIsWritable(t *testing.T) {
	t.Parallel()

	definitions := []*resourcesv1.VariableDefinition{
		def("API_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		scoped("POSTHOG_ID", "/web"),
	}

	t.Run("a declared key is writable", func(t *testing.T) {
		t.Parallel()
		if err := variables.RefuseUnwritable(definitions, "API_URL", ""); err != nil {
			t.Fatalf("RefuseUnwritable(API_URL) = %v, want a declared key writable", err)
		}
	})

	t.Run("a key no app declares is refused", func(t *testing.T) {
		t.Parallel()
		err := variables.RefuseUnwritable(definitions, "SITE_HOSTNAME", "")
		if err == nil {
			t.Fatal("RefuseUnwritable(SITE_HOSTNAME) = nil, want a key nothing declares refused rather than stored where nothing reads it")
		}
		for _, want := range []string{"SITE_HOSTNAME", "defineEnv"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q named", err, want)
			}
		}
	})

	t.Run("a key no app declares is refused in a folder too", func(t *testing.T) {
		t.Parallel()
		if err := variables.RefuseUnwritable(definitions, "SITE_HOSTNAME", "/web"); err == nil {
			t.Fatal("RefuseUnwritable(SITE_HOSTNAME, /web) = nil, want a folder to be no way around an undeclared key")
		}
	})
}
