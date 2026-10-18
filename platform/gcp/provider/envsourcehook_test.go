package gcp

import "testing"

func TestAGCPTargetProvesItsIdentityAsItsOwnServiceAccount(t *testing.T) {
	p, err := NewProvider(Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Hooks().EnvSource == nil {
		t.Fatal("the gcp provider sets no EnvSource hooks, so every Infisical env source with identity auth is refused on a gcp target")
	}
}
