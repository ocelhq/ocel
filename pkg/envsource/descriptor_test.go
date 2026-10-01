package envsource_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func deployed(t *testing.T, kind, options string) envsource.Descriptor {
	t.Helper()
	descriptor, err := envsource.NewDescriptor(kind, []byte(options))
	if err != nil {
		t.Fatalf("NewDescriptor(%s, %s) = %v", kind, options, err)
	}
	return descriptor
}

func dev(t *testing.T, kind, options string) envsource.Descriptor {
	t.Helper()
	descriptor, err := envsource.NewDevDescriptor(kind, []byte(options))
	if err != nil {
		t.Fatalf("NewDevDescriptor(%s, %s) = %v", kind, options, err)
	}
	return descriptor
}

const (
	universalInfisical = `{"project":"p-1","environment":"prod","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"SECRET"}}}}`
	identityInfisical  = `{"project":"p-1","environment":"prod","auth":{"identity":{"identityId":"ident"}}}`
)

func TestAKindOcelDoesNotKnowIsRefusedByName(t *testing.T) {
	for _, decode := range []func(string, json.RawMessage) (envsource.Descriptor, error){envsource.NewDescriptor, envsource.NewDevDescriptor} {
		_, err := decode("vault", []byte(`{}`))
		if err == nil || !strings.Contains(err.Error(), `"vault"`) {
			t.Errorf("decoding vault = %v, want a refusal naming it", err)
		}
	}
}

func TestEachTierReadsOnlyTheKindsThatServeIt(t *testing.T) {
	if _, err := envsource.NewDescriptor(envsource.Dotenv, nil); err == nil || !strings.Contains(err.Error(), "dotenv") {
		t.Errorf("a deployed dotenv = %v, want it refused: .env files are read on the developer's machine", err)
	}
	if _, err := envsource.NewDevDescriptor(envsource.Builtin, nil); err == nil || !strings.Contains(err.Error(), "builtin") {
		t.Errorf("a dev builtin = %v, want it refused: ocel's own store serves production and preview", err)
	}
	if _, err := envsource.NewDescriptor(envsource.Builtin, []byte(`{"project":"p"}`)); err == nil {
		t.Error("builtin with options decoded, want it refused: it takes none")
	}
}

func TestMalformedOptionsAreRefusedNamingTheFieldAtFault(t *testing.T) {
	for _, c := range []struct {
		name, kind, options, field string
	}{
		{"not JSON", "infisical", `{"project":`, ""},
		{"not an object", "infisical", `["p"]`, ""},
		{"data after the options", "exec", `{"command":["a"],"format":"json"}garbage`, ""},
		{"a second object after the options", "exec", `{"command":["a"],"format":"json"} {}`, ""},
		{"an unknown field", "infisical", `{"project":"p","environment":"e","auth":{"identity":{"identityId":"i"}},"region":"eu"}`, "region"},
		{"no project", "infisical", `{"environment":"prod","auth":{"identity":{"identityId":"i"}}}`, "project"},
		{"a blank project", "infisical", `{"project":"  ","environment":"prod","auth":{"identity":{"identityId":"i"}}}`, "project"},
		{"no environment", "infisical", `{"project":"p","auth":{"identity":{"identityId":"i"}}}`, "environment"},
		{"a host that is no http URL", "infisical", `{"project":"p","environment":"e","host":"infisical.example.com","auth":{"identity":{"identityId":"i"}}}`, "host"},
		{"no auth", "infisical", `{"project":"p","environment":"e"}`, "auth"},
		{"auth keyed by nothing", "infisical", `{"project":"p","environment":"e","auth":{}}`, "auth"},
		{"auth keyed twice", "infisical", `{"project":"p","environment":"e","auth":{"identity":{"identityId":"i"},"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"S"}}}}`, "auth"},
		{"an identity with no id", "infisical", `{"project":"p","environment":"e","auth":{"identity":{}}}`, "auth.identity.identityId"},
		{"universal auth with no client id", "infisical", `{"project":"p","environment":"e","auth":{"universal":{"clientSecret":{"$env":"S"}}}}`, "auth.universal.clientId"},
		{"universal auth naming an empty variable", "infisical", `{"project":"p","environment":"e","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":""}}}}`, "auth.universal.clientSecret"},
		{"universal auth naming a variable with a control character", "infisical", `{"project":"p","environment":"e","auth":{"universal":{"clientId":{"$env":"I\nD"},"clientSecret":{"$env":"S"}}}}`, "auth.universal.clientId"},
		{"universal auth naming a variable with a #", "infisical", `{"project":"p","environment":"e","auth":{"universal":{"clientId":{"$env":"ID"},"clientSecret":{"$env":"S#1"}}}}`, "auth.universal.clientSecret"},
		{"a write policy ocel does not know", "infisical", `{"project":"p","environment":"e","auth":{"identity":{"identityId":"i"}},"write":"always"}`, "write"},
		{"exec with no command", "exec", `{"command":[],"format":"json"}`, "command"},
		{"exec with no format", "exec", `{"command":["vault"]}`, "format"},
		{"exec with a format ocel does not read", "exec", `{"command":["vault"],"format":"yaml"}`, "format"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := envsource.NewDescriptor(c.kind, []byte(c.options))
			if err == nil {
				t.Fatalf("decoded %s", c.options)
			}
			var refused *envsource.OptionError
			if !errors.As(err, &refused) || refused.Field != c.field {
				t.Fatalf("NewDescriptor() = %v, want an OptionError at %q", err, c.field)
			}
		})
	}
}

func TestADevInfisicalReadsAsTheDeveloperSoTakesNoAuthAndNeverWrites(t *testing.T) {
	for options, field := range map[string]string{
		`{"project":"p","environment":"dev","auth":{"identity":{"identityId":"i"}}}`: "auth",
		`{"project":"p","environment":"dev","write":"missing"}`:                      "write",
	} {
		_, err := envsource.NewDevDescriptor("infisical", []byte(options))
		var refused *envsource.OptionError
		if !errors.As(err, &refused) || refused.Field != field {
			t.Errorf("NewDevDescriptor(%s) = %v, want an OptionError at %q", options, err, field)
		}
	}
	if got := dev(t, "infisical", `{"project":"p","environment":"dev"}`).Reading(); got != envsource.ReadingWhereOcelRuns {
		t.Errorf("a dev infisical is read %v, want where ocel runs", got)
	}
}

func TestEachKindIsReadWhereItsValuesLive(t *testing.T) {
	for _, c := range []struct {
		name       string
		descriptor envsource.Descriptor
		want       envsource.Reading
	}{
		{"builtin", deployed(t, envsource.Builtin, ""), envsource.ReadingOwnStore},
		{"dotenv", dev(t, envsource.Dotenv, ""), envsource.ReadingOwnStore},
		{"exec", deployed(t, "exec", `{"command":["vault"],"format":"json"}`), envsource.ReadingWhereOcelRuns},
		{"a dev exec", dev(t, "exec", `{"command":["vault"],"format":"json"}`), envsource.ReadingWhereOcelRuns},
		{"infisical", deployed(t, "infisical", identityInfisical), envsource.ReadingOnSchedule},
	} {
		if got := c.descriptor.Reading(); got != c.want {
			t.Errorf("%s is read %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOcelCreatesInInfisicalUnderMissingOrValuesAndUpdatesOnlyUnderValues(t *testing.T) {
	writing := func(write string) envsource.Descriptor {
		return deployed(t, "infisical", `{"project":"p","environment":"e","auth":{"identity":{"identityId":"i"}}`+write+`}`)
	}
	for _, c := range []struct {
		name             string
		descriptor       envsource.Descriptor
		creates, updates bool
	}{
		{"infisical writing values", writing(`,"write":"values"`), true, true},
		{"infisical writing missing keys", writing(`,"write":"missing"`), true, false},
		{"infisical never writing", writing(`,"write":"never"`), false, false},
		{"infisical left to its default", writing(""), false, false},
		{"exec", deployed(t, "exec", `{"command":["vault"],"format":"json"}`), false, false},
		{"builtin", deployed(t, envsource.Builtin, ""), false, false},
	} {
		if got := c.descriptor.CanCreate(); got != c.creates {
			t.Errorf("%s CanCreate() = %v, want %v", c.name, got, c.creates)
		}
		if got := c.descriptor.CanUpdate(); got != c.updates {
			t.Errorf("%s CanUpdate() = %v, want %v", c.name, got, c.updates)
		}
	}
}

func TestAnEnvSourceLogsInWithTheOcelVariablesItsAuthNames(t *testing.T) {
	if got := deployed(t, "infisical", universalInfisical).CredentialVariables(); !slices.Equal(got, []string{"ID", "SECRET"}) {
		t.Errorf("universal CredentialVariables() = %v, want ID and SECRET", got)
	}
	for _, descriptor := range []envsource.Descriptor{
		deployed(t, envsource.Builtin, ""),
		deployed(t, "exec", `{"command":["true"],"format":"json"}`),
		deployed(t, "infisical", identityInfisical),
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
		{deployed(t, envsource.Builtin, ""), "builtin"},
		{dev(t, envsource.Dotenv, ""), "dotenv"},
		{deployed(t, "exec", `{"command":["vault"],"format":"json"}`), "exec"},
		{deployed(t, "infisical", `{"project":"p-1","environment":"prod","path":"/acme","auth":{"identity":{"identityId":"i"}}}`), "infisical:p-1/prod"},
	} {
		if got := c.descriptor.ID(); got != c.want {
			t.Errorf("%s ID() = %q, want %q", c.descriptor.Kind(), got, c.want)
		}
	}
}

func TestADescriptorTravelsAsItsKindAndOptionsAndDecodesAgainOnArrival(t *testing.T) {
	sent := deployed(t, "infisical", universalInfisical)
	encoded, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	var arrived envsource.Descriptor
	if err := json.Unmarshal(encoded, &arrived); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", encoded, err)
	}
	if arrived.Kind() != "infisical" || arrived.ID() != "infisical:p-1/prod" || !slices.Equal(arrived.CredentialVariables(), []string{"ID", "SECRET"}) {
		t.Errorf("arrived as %s %s %v, want the infisical descriptor sent", arrived.Kind(), arrived.ID(), arrived.CredentialVariables())
	}
	if err := json.Unmarshal([]byte(`{"kind":"infisical","options":{"project":"p"}}`), &arrived); err == nil {
		t.Error("a stored descriptor with malformed options decoded, want it refused")
	}
}

func TestADescriptorKeepsItsOptionsAsOcelSpellsThem(t *testing.T) {
	for sent, want := range map[string]string{
		`{"Command":["a"],"FORMAT":"json"}`:                     `{"command":["a"],"format":"json"}`,
		`{"command":["a"],"command":["b"],"format":"json"}`:     `{"command":["b"],"format":"json"}`,
		`{ "format" : "dotenv", "command" : [ "op", "read" ] }`: `{"command":["op","read"],"format":"dotenv"}`,
	} {
		if got := string(deployed(t, "exec", sent).Options()); got != want {
			t.Errorf("Options() of %s = %s, want %s", sent, got, want)
		}
	}
}

func TestNormalizeRootsThePathAndFillsTheCloudHostAndNeverWriting(t *testing.T) {
	got := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "acme//web/"}.Normalize()
	want := envsource.InfisicalOptions{Project: "p", Environment: "prod", Path: "/acme/web", Host: "https://app.infisical.com", Write: envsource.WriteNever}
	if got.Project != want.Project || got.Environment != want.Environment || got.Path != want.Path || got.Host != want.Host || got.Write != want.Write {
		t.Errorf("Normalize() = %+v, want %+v", got, want)
	}
}
