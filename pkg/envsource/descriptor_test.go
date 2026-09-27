package envsource_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func TestOnlyInfisicalIsSyncedOnASchedule(t *testing.T) {
	for kind, want := range map[envsource.Kind]bool{
		envsource.Builtin:   false,
		envsource.Dotenv:    false,
		envsource.Exec:      false,
		envsource.Infisical: true,
	} {
		if got := (envsource.Descriptor{Kind: kind}).IsScheduled(); got != want {
			t.Errorf("%s IsScheduled() = %v, want %v", kind, got, want)
		}
	}
}

func TestADeployWritesIntoInfisicalOnlyWhenItsWritePolicyIsMissing(t *testing.T) {
	for _, c := range []struct {
		name       string
		descriptor envsource.Descriptor
		want       bool
	}{
		{"infisical writing missing keys", envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Write: envsource.WriteMissing}}, true},
		{"infisical never writing", envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Write: envsource.WriteNever}}, false},
		{"exec", envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{}}, false},
		{"builtin", envsource.Descriptor{Kind: envsource.Builtin}, false},
	} {
		if got := c.descriptor.CanWrite(); got != c.want {
			t.Errorf("%s CanWrite() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOnlyUniversalAuthReadsOcelVariables(t *testing.T) {
	universal := envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "ID", ClientSecretVariable: "SECRET"}
	if got := universal.Variables(); !slices.Equal(got, []string{"ID", "SECRET"}) {
		t.Errorf("universal Variables() = %v, want ID and SECRET", got)
	}
	if got := (envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "ident"}).Variables(); got != nil {
		t.Errorf("identity Variables() = %v, want none", got)
	}
}

func TestNormalizeRootsThePathAndFillsTheCloudHostAndNeverWriting(t *testing.T) {
	got := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "acme//web/"}.Normalize()
	want := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "/acme/web", Host: "https://app.infisical.com", Write: envsource.WriteNever}
	if got != want {
		t.Errorf("Normalize() = %+v, want %+v", got, want)
	}
}
