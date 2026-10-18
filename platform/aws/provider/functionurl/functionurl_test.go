package functionurl

import (
	"slices"
	"testing"
)

func TestPublic(t *testing.T) {
	t.Parallel()

	t.Run("grants both actions AWS requires of a public URL, each held to the URL", func(t *testing.T) {
		t.Parallel()

		permissions := Public()
		actions := make([]string, 0, len(permissions))
		for _, p := range permissions {
			actions = append(actions, p.Action)
			switch p.Action {
			case InvokeURLAction:
				if p.URLAuthType != AuthNone || p.ViaURL {
					t.Errorf("%s = %+v, want it held to an unsigned URL by its auth type", p.Action, p)
				}
			case InvokeAction:
				if !p.ViaURL || p.URLAuthType != "" {
					t.Errorf("%s = %+v, want it held to calls through the URL", p.Action, p)
				}
			}
		}
		slices.Sort(actions)
		if want := []string{InvokeAction, InvokeURLAction}; !slices.Equal(actions, want) {
			t.Errorf("Public grants %v, want %v: since October 2025 a URL missing either answers 403", actions, want)
		}
	})

	t.Run("names each permission apart", func(t *testing.T) {
		t.Parallel()

		seen := map[string]bool{}
		for _, p := range Public() {
			if seen[p.Verb()] {
				t.Errorf("two permissions are both named %s", p.Verb())
			}
			seen[p.Verb()] = true
		}
	})
}

func TestInvokerActions(t *testing.T) {
	t.Parallel()

	got := InvokerActions()
	if !slices.Contains(got, InvokeURLAction) || !slices.Contains(got, InvokeAction) {
		t.Errorf("InvokerActions = %v, want both %s and %s, which a signed caller needs since October 2025", got, InvokeURLAction, InvokeAction)
	}
}
