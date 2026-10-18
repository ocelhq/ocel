package variablestoreserver_test

import (
	"errors"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func TestAGrantOfEveryActionOfAServiceOrOneActionOfEveryServiceIsUnscoped(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"s3:*", "*:GetObject", "*"} {
		binding := &bindingsv1.Binding{Name: "uploads", Grants: []*bindingsv1.Grant{{
			Label:     "read",
			Actions:   []string{"s3:GetObject", action},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}}}
		if err := variablestoreserver.VerifyGrantScope(binding); !errors.Is(err, provider.ErrUnscopedGrant) {
			t.Errorf("VerifyGrantScope(%q) = %v, want it refused as unscoped: a binding grants an app what it needs of the resource it binds", action, err)
		}
	}
}

func TestAGrantNamingItsActionsAndResourcesIsInScope(t *testing.T) {
	t.Parallel()

	binding := &bindingsv1.Binding{Name: "uploads", Grants: []*bindingsv1.Grant{{
		Label:     "read",
		Actions:   []string{"s3:GetObject", "storage.objects.get"},
		Resources: []string{"arn:aws:s3:::uploads/*"},
	}}}
	if err := variablestoreserver.VerifyGrantScope(binding); err != nil {
		t.Errorf("VerifyGrantScope() = %v, want a grant naming its actions and resources accepted", err)
	}
}
