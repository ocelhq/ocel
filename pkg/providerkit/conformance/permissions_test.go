package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type permissionsPort struct{ err error }

func (permissionsPort) Whoami(context.Context) (provider.Principal, error) {
	return provider.Principal{Vendor: "test"}, nil
}

func (s permissionsPort) Permissions(edge.CredentialTier) (edge.CredentialDocument, error) {
	return edge.CredentialDocument{}, s.err
}

func TestPermissionsMayBeUnwrittenSoLongAsTheProviderSaysSo(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		err  error
		held bool
	}{
		"a document":              {err: nil, held: true},
		"none written yet":        {err: refusal.Refuse(refusal.CodeNotReady, "no permissions document yet"), held: true},
		"a tier it will not name": {err: refusal.Refuse(refusal.CodeInvalid, "no such tier"), held: false},
		"something broken":        {err: errors.New("the document would not render"), held: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := permissionsRendered(permissionsPort{err: tc.err}, edge.TierBootstrap)
			if held := err == nil; held != tc.held {
				t.Errorf("permissionsRendered() = %v, want held = %v", err, tc.held)
			}
		})
	}
}
