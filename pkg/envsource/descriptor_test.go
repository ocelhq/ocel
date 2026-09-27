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

func TestAnEnvSourceIsNamedByItsKindAndAnInfisicalOneByItsProjectAndEnvironment(t *testing.T) {
	for _, c := range []struct {
		descriptor envsource.Descriptor
		want       string
	}{
		{envsource.Descriptor{Kind: envsource.Builtin}, "builtin"},
		{envsource.Descriptor{Kind: envsource.Dotenv}, "dotenv"},
		{envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"vault"}}}, "exec"},
		{envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Project: "p-1", Environment: "prod", Path: "/acme"}}, "infisical:p-1/prod"},
	} {
		if got := c.descriptor.ID(); got != c.want {
			t.Errorf("%s ID() = %q, want %q", c.descriptor.Kind, got, c.want)
		}
	}
}

func TestNormalizeRootsThePathAndFillsTheCloudHostAndNeverWriting(t *testing.T) {
	got := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "acme//web/"}.Normalize()
	want := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "/acme/web", Host: "https://app.infisical.com", Write: envsource.WriteNever}
	if got != want {
		t.Errorf("Normalize() = %+v, want %+v", got, want)
	}
}
