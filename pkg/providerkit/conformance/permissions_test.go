package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type permissionsPort struct{ err error }

func (permissionsPort) Whoami(context.Context) (providerkit.Identity, error) {
	return providerkit.Identity{Provider: "test"}, nil
}

func (s permissionsPort) Permissions(providerkit.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{}, s.err
}

func TestPermissionsMayBeUnwrittenSoLongAsTheProviderSaysSo(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		held bool
	}{
		"a document":              {err: nil, held: true},
		"none written yet":        {err: providerkit.Refuse(providerkit.CodeNotReady, "no permissions document yet"), held: true},
		"a tier it will not name": {err: providerkit.Refuse(providerkit.CodeInvalid, "no such tier"), held: false},
		"something broken":        {err: errors.New("the document would not render"), held: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := permissionsRendered(permissionsPort{err: tc.err}, providerkit.TierBootstrap)
			if held := err == nil; held != tc.held {
				t.Errorf("permissionsRendered() = %v, want held = %v", err, tc.held)
			}
		})
	}
}
