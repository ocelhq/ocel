package env

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/pkg/envsource"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const infisicalConfig = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
    preview: { exec: { command: ["sh", "-c", "printf 'LOG_LEVEL=debug'"], format: "dotenv" } },
  },
};
`

var infisicalProduction = envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
	Project: "p-1", Environment: "prod", Path: "/", Host: "https://app.infisical.com", Write: envsource.WriteNever,
	Auth: envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "INFISICAL_CLIENT_ID", ClientSecretVariable: "INFISICAL_CLIENT_SECRET"},
}}

func setUpEnvSourceFixture(t *testing.T, source clitest.FakeEnvSource) string {
	t.Helper()
	root := setUpEnvFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalConfig)
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.FakeEnvSourceEnvVar, string(raw))
	return root
}

func registerFakeEnvSource(t *testing.T, tier environmentv1.Tier, descriptor envsource.Descriptor) {
	t.Helper()
	registrations, err := clitest.LoadFakeRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	registrations[clitest.FakeRegistrationKey(tier, clitest.FixtureSlug)] = clitest.FakeRegistration{Descriptor: descriptor, Folders: []string{""}}
	if err := clitest.SaveFakeRegistrations(registrations); err != nil {
		t.Fatal(err)
	}
}

func setCredentials(t *testing.T, root string) {
	t.Helper()
	envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "client-secret", envOptions{})
}

func TestEnvSetTakesTheCredentialsATiersEnvSourceLogsInWith(t *testing.T) {
	root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{})
	if out := envSet(t, root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{}); !strings.Contains(out, "INFISICAL_CLIENT_SECRET") {
		t.Errorf("set stdout = %q, want the credential set though no app declares it", out)
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), streamedDeps(&stderr), root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{folder: "/web"}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "infisical:p-1/prod") {
		t.Fatalf("runEnvSet --folder err = %v, want a credential in a folder refused, naming the env source", err)
	}

	t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
	stderr.Reset()
	err = runEnvSet(context.Background(), streamedDeps(&stderr), root, "INFISICAL_CLIENT_SECRET", "secret", envOptions{preview: true}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "declares") {
		t.Fatalf("runEnvSet --preview err = %v, want preview's exec source to have no credential to take", err)
	}
}

func TestEnvSyncReReadsTheEnvSourceADeployRegistered(t *testing.T) {
	t.Run("a tier nothing registered has nothing to sync, and says a deploy reads what the config names", func(t *testing.T) {
		root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		if err := runEnvSync(context.Background(), streamedDeps(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"builtin", "nothing to sync", "infisical:p-1/prod", "ocel deploy"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}
	})

	t.Run("a registered source is read now, and ls names it as each value's source", func(t *testing.T) {
		root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "API_TOKEN", Value: "t"}},
		})
		setCredentials(t, root)
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})
		registerFakeEnvSource(t, environmentv1.Tier_TIER_PRODUCTION, infisicalProduction)

		var stdout, stderr bytes.Buffer
		if err := runEnvSync(context.Background(), streamedDeps(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "infisical:p-1/prod") || !strings.Contains(out, "2 written") {
			t.Errorf("stdout = %q, want the env source named and both values written", out)
		}

		var ls bytes.Buffer
		if err := runEnvLs(context.Background(), streamedDeps(&ls), root, envOptions{}, &ls, &ls); err != nil {
			t.Fatalf("runEnvLs err = %v; out=%s", err, ls.String())
		}
		sources := map[string]string{}
		for _, line := range strings.Split(ls.String(), "\n") {
			if fields := strings.Fields(line); len(fields) > 1 {
				sources[fields[0]] = fields[len(fields)-1]
			}
		}
		for key, want := range map[string]string{"STRIPE_API_KEY": "infisical:p-1/prod", "INFISICAL_CLIENT_ID": "builtin"} {
			if sources[key] != want {
				t.Errorf("SOURCE of %s = %q, want %q; ls:\n%s", key, sources[key], want, ls.String())
			}
		}
		if _, listed := sources["LOG_LEVEL"]; listed {
			t.Errorf("LOG_LEVEL, set in ocel's own store before the tier read from an env source that lacks it, is still listed:\n%s", ls.String())
		}
	})

	t.Run("a registered exec source is re-read only by a deploy", func(t *testing.T) {
		root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{})
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		registerFakeEnvSource(t, environmentv1.Tier_TIER_PREVIEW, envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"sh"}}})

		var stdout, stderr bytes.Buffer
		err := runEnvSync(context.Background(), streamedDeps(&stderr), root, envOptions{preview: true}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), "deploy") {
			t.Fatalf("runEnvSync --preview err = %v, want exec's re-read left to a deploy", err)
		}
	})
}

func TestEnvSourceDescribesWhereATierReadsFrom(t *testing.T) {
	root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{
		URLs:          map[string]string{"": "https://infisical.example/prod"},
		LastAttemptAt: 1_700_000_120,
		LastSuccessAt: 1_700_000_000,
		LastError:     "Infisical answered 503",
	})

	var before bytes.Buffer
	if err := runEnvSource(context.Background(), streamedDeps(&before), root, envOptions{}, &before, &before); err != nil {
		t.Fatalf("runEnvSource err = %v; out=%s", err, before.String())
	}
	if out := before.String(); !strings.Contains(out, "production reads from builtin") || !strings.Contains(out, "infisical:p-1/prod") {
		t.Errorf("stdout = %q, want builtin named beside the env source the config names", out)
	}
	if out := before.String(); !strings.Contains(out, "dev reads from dotenv, then .env.local on top") {
		t.Errorf("stdout = %q, want the dev tier's env source named with .env.local over it", out)
	}

	setCredentials(t, root)
	registerFakeEnvSource(t, environmentv1.Tier_TIER_PRODUCTION, infisicalProduction)
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), streamedDeps(&synced), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}

	var stdout, stderr bytes.Buffer
	if err := runEnvSource(context.Background(), streamedDeps(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"production reads from infisical:p-1/prod", "synced every minute", "Infisical answered 503", "https://infisical.example/prod", "INFISICAL_CLIENT_SECRET"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "configured") {
		t.Errorf("stdout = %q, want no note when the registered env source is the configured one", out)
	}
}

func TestEnvSourceSaysWhatOcelMayWriteIntoIt(t *testing.T) {
	root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{})
	for write, want := range map[envsource.WritePolicy]string{
		envsource.WriteValues:  "create or update a value there, never delete one",
		envsource.WriteMissing: "created there, never overwritten",
		envsource.WriteNever:   "",
	} {
		options := *infisicalProduction.Infisical
		options.Write = write
		registerFakeEnvSource(t, environmentv1.Tier_TIER_PRODUCTION, envsource.Descriptor{Kind: envsource.Infisical, Infisical: &options})
		var stdout, stderr bytes.Buffer
		if err := runEnvSource(context.Background(), streamedDeps(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvSource err = %v; stderr=%s", err, stderr.String())
		}
		if writes := strings.Contains(stdout.String(), "writes"); writes != (want != "") || !strings.Contains(stdout.String(), want) {
			t.Errorf("write %q: stdout = %q, want %q", write, stdout.String(), want)
		}
	}
}

func TestEnvSetOnAValueTheEnvSourceOwnsSaysWhereToChangeIt(t *testing.T) {
	root := setUpEnvSourceFixture(t, clitest.FakeEnvSource{Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}}})
	setCredentials(t, root)
	registerFakeEnvSource(t, environmentv1.Tier_TIER_PRODUCTION, infisicalProduction)
	var synced bytes.Buffer
	if err := runEnvSync(context.Background(), streamedDeps(&synced), root, envOptions{}, &synced, &synced); err != nil {
		t.Fatalf("runEnvSync err = %v; out=%s", err, synced.String())
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), streamedDeps(&stderr), root, "STRIPE_API_KEY", "by-hand", envOptions{}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), "infisical:p-1/prod") {
		t.Fatalf("runEnvSet err = %v, want the env source that owns the value named", err)
	}
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "rotated", envOptions{})
}

func writing(write envsource.WritePolicy) envsource.Descriptor {
	options := *infisicalProduction.Infisical
	options.Write = write
	return envsource.Descriptor{Kind: envsource.Infisical, Infisical: &options}
}

func productionRegistration(t *testing.T) clitest.FakeRegistration {
	t.Helper()
	registrations, err := clitest.LoadFakeRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	return registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]
}

func TestEnvSetUpdatesAValueTheEnvSourceOwnsWhenItsWritePolicyIsValues(t *testing.T) {
	root := syncedEnvSourceFixture(t, writing(envsource.WriteValues))

	out := envSet(t, root, "STRIPE_API_KEY", "sk_rotated", envOptions{})
	if !strings.Contains(out, "infisical:p-1/prod") {
		t.Errorf("stdout = %q, want the env source it was written to named", out)
	}
	if updated := productionRegistration(t).Updated; !slices.Contains(updated, clitest.FakeEnvSourceValue{Key: "STRIPE_API_KEY", Value: "sk_rotated"}) {
		t.Fatalf("updated = %+v, want STRIPE_API_KEY written through to the env source", updated)
	}
	var got, chatter bytes.Buffer
	if err := runEnvGet(context.Background(), streamedDeps(&chatter), root, "STRIPE_API_KEY", envOptions{reveal: true, yes: true}, &got, &chatter); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.String(), "sk_rotated") {
		t.Errorf("ocel env get = %q, want the value synced straight back", got.String())
	}
}

func TestEnvSetCreatesAValueTheEnvSourceLacksWhenItsWritePolicyIsMissing(t *testing.T) {
	root := syncedEnvSourceFixture(t, writing(envsource.WriteMissing))

	envSet(t, root, "API_TOKEN", "tok", envOptions{})
	if created := productionRegistration(t).Created; !slices.Contains(created, clitest.FakeEnvSourceValue{Key: "API_TOKEN", Value: "tok"}) {
		t.Fatalf("created = %+v, want API_TOKEN created in the env source", created)
	}

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), streamedDeps(&stderr), root, "STRIPE_API_KEY", "sk_rotated", envOptions{}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(stderr.String(), `"values"`) {
		t.Fatalf("runEnvSet over a value the env source holds = %v, want it refused naming write \"values\"", err)
	}
}

func TestEnvSetKeepsANamedEnvironmentsOverrideAndACredentialInOcelsOwnStore(t *testing.T) {
	root := syncedEnvSourceFixture(t, writing(envsource.WriteValues))

	envSet(t, root, "INFISICAL_CLIENT_SECRET", "rotated", envOptions{})
	if registration := productionRegistration(t); len(registration.Updated)+len(registration.Created) != 0 {
		t.Fatalf("registration = %+v, want a credential never written to the env source", registration)
	}
}

func TestTheSourceColumnNamesWhereEachValueComesFrom(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	renderValues(&stdout, []*envvarsv1.ValueMetadata{
		{Coordinate: &envvarsv1.Coordinate{Key: "OWN"}},
		{Coordinate: &envvarsv1.Coordinate{Key: "COPIED"}, EnvSource: "infisical:p-1/prod"},
		{Coordinate: &envvarsv1.Coordinate{Key: "SHARED"}, Target: &envvarsv1.Coordinate{Slug: "platform", Key: "SHARED"}},
	}, nil, nil, nil)

	sources := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			sources[fields[0]] = fields[len(fields)-1]
		}
	}
	for key, want := range map[string]string{"OWN": "builtin", "COPIED": "infisical:p-1/prod", "SHARED": "platform/SHARED"} {
		if sources[key] != want {
			t.Errorf("SOURCE of %s = %q, want %q; ls:\n%s", key, sources[key], want, stdout.String())
		}
	}
}
