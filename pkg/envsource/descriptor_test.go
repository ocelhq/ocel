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

func TestOcelCreatesInInfisicalUnderMissingOrValuesAndUpdatesOnlyUnderValues(t *testing.T) {
	writing := func(write envsource.WritePolicy) envsource.Descriptor {
		return envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Write: write}}
	}
	for _, c := range []struct {
		name             string
		descriptor       envsource.Descriptor
		creates, updates bool
	}{
		{"infisical writing values", writing(envsource.WriteValues), true, true},
		{"infisical writing missing keys", writing(envsource.WriteMissing), true, false},
		{"infisical never writing", writing(envsource.WriteNever), false, false},
		{"exec", envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{}}, false, false},
		{"builtin", envsource.Descriptor{Kind: envsource.Builtin}, false, false},
	} {
		if got := c.descriptor.CanCreate(); got != c.creates {
			t.Errorf("%s CanCreate() = %v, want %v", c.name, got, c.creates)
		}
		if got := c.descriptor.CanUpdate(); got != c.updates {
			t.Errorf("%s CanUpdate() = %v, want %v", c.name, got, c.updates)
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

func TestAnEnvSourceLogsInWithTheOcelVariablesItsAuthNames(t *testing.T) {
	universal := envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
		Auth: envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "ID", ClientSecretVariable: "SECRET"},
	}}
	if got := universal.CredentialVariables(); !slices.Equal(got, []string{"ID", "SECRET"}) {
		t.Errorf("universal CredentialVariables() = %v, want ID and SECRET", got)
	}
	for _, descriptor := range []envsource.Descriptor{
		{Kind: envsource.Builtin},
		{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"true"}}},
		{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{Auth: envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "ident"}}},
	} {
		if got := descriptor.CredentialVariables(); got != nil {
			t.Errorf("%s CredentialVariables() = %v, want none", descriptor.ID(), got)
		}
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
