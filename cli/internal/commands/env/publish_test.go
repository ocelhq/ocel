package env

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/outputschema"
	"github.com/ocelhq/ocel/cli/internal/outputschema/outputschematest"
	"github.com/ocelhq/ocel/pkg/environment"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const infraPublisher = "urn:pulumi:prod::infra"

func envPublish(t *testing.T, root, published string, opts envOptions) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runEnvPublish(context.Background(), newStreamedDependencies(&stderr), root, infraPublisher, strings.NewReader(published), opts, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func production() variablestore.Scope {
	return variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
}

func TestPublishingValuesWritesEachDeclaredOneUnderItsPublisher(t *testing.T) {
	project := setUpEnvFixture(t)

	stdout, stderr, err := envPublish(t, project.Root, `{"values":[
		{"key":"STRIPE_API_KEY","value":"sk_live_secret","secret":true},
		{"key":"LOG_LEVEL","value":"debug"}
	]}`, envOptions{})
	if err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "2") || !strings.Contains(stdout, infraPublisher) {
		t.Errorf("publish stdout = %q, want the count written and the publisher named", stdout)
	}
	for key, want := range map[string]struct {
		value  string
		secret bool
	}{"STRIPE_API_KEY": {"sk_live_secret", true}, "LOG_LEVEL": {"debug", false}} {
		got, err := valuesOf(project).Get(context.Background(), production(), cellAt(key), true)
		if err != nil || got.Plaintext != want.value || got.Provenance.Publisher != infraPublisher || got.Provenance.PublishedSecret != want.secret {
			t.Errorf("%s = %+v, %v, want %q published by %s, secret %t", key, got, err, want.value, infraPublisher, want.secret)
		}
	}
}

func TestPublishingValuesAsJSONNamesWhatItWroteAndRemovedAndNeverAValue(t *testing.T) {
	project := setUpEnvFixture(t)
	if _, stderr, err := envPublish(t, project.Root, `{"values":[{"key":"LOG_LEVEL","value":"debug"}]}`, envOptions{}); err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
	}

	var stdout, stderr bytes.Buffer
	published := `{"values":[{"key":"STRIPE_API_KEY","value":"sk_live_secret","secret":true}]}`
	if err := runEnvPublish(context.Background(), newJSONDependencies(&stderr), project.Root, infraPublisher, strings.NewReader(published), envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr.String())
	}

	schema, err := outputschema.ComposeResult(new(resultv1.EnvPublishResult).ProtoReflect().Descriptor().FullName())
	if err != nil {
		t.Fatal(err)
	}
	if err := outputschematest.Validate(outputschematest.Compile(t, schema), stdout.Bytes()); err != nil {
		t.Errorf("the env publish schema rejects what env publish prints: %v\n%s", err, stdout.String())
	}
	for _, want := range []string{"STRIPE_API_KEY", "LOG_LEVEL", infraPublisher, `"version":"1"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %s, want %q named", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "sk_live_secret") {
		t.Errorf("stdout = %s, want no value printed", stdout.String())
	}
}

func TestPublishingAValueRefusesWhatAHandSetOneWouldBeRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		published string
		named     []string
	}{
		"a key no app declares": {
			published: `{"values":[{"key":"LOG_LEVEL","value":"debug"},{"key":"SITE_HOSTNAME","value":"acme.example"}]}`,
			named:     []string{"SITE_HOSTNAME", "defineEnv"},
		},
		"a secret into a variable declared plain": {
			published: `{"values":[{"key":"LOG_LEVEL","value":"debug","secret":true}]}`,
			named:     []string{"LOG_LEVEL", "plain", "secret"},
		},
		"a plain value into a variable declared secret": {
			published: `{"values":[{"key":"STRIPE_API_KEY","value":"sk_live_secret"}]}`,
			named:     []string{"STRIPE_API_KEY", "secret"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			project := setUpEnvFixture(t)

			_, stderr, err := envPublish(t, project.Root, tc.published, envOptions{})
			if err == nil {
				t.Fatal("runEnvPublish err = nil, want it refused")
			}
			for _, want := range tc.named {
				if !strings.Contains(stderr, want) {
					t.Errorf("stream = %q, want %q named", stderr, want)
				}
			}
			listed, err := valuesOf(project).List(context.Background(), production())
			if err != nil || len(listed) != 0 {
				t.Errorf("values after a refused publish = %+v, %v, want none written", listed, err)
			}
		})
	}
}

func TestThePublishHelpNamesBothClassesAPublishedValueMustMatch(t *testing.T) {
	long := newPublishCommand(Dependencies{}).Long
	for _, want := range []string{"a secret value must be declared secret", "a plain value must not be"} {
		if !strings.Contains(long, want) {
			t.Errorf("ocel env publish help = %q, want it to say %q", long, want)
		}
	}
}

func TestPublishingNothingRemovesEveryValueThePublisherWrote(t *testing.T) {
	project := setUpEnvFixture(t)
	if _, stderr, err := envPublish(t, project.Root, `{"values":[{"key":"LOG_LEVEL","value":"debug"}]}`, envOptions{}); err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
	}

	stdout, stderr, err := envPublish(t, project.Root, `{}`, envOptions{})
	if err != nil {
		t.Fatalf("runEnvPublish of nothing err = %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOG_LEVEL") {
		t.Errorf("publish stdout = %q, want the removed key named", stdout)
	}
	if _, err := valuesOf(project).Get(context.Background(), production(), cellAt("LOG_LEVEL"), false); !errors.Is(err, variablestore.ErrNotFound) {
		t.Errorf("LOG_LEVEL = %v, want it removed", err)
	}
}

func TestAValueAPublisherWroteIsChangedOnlyInItsProgram(t *testing.T) {
	project := setUpEnvFixture(t)
	if _, stderr, err := envPublish(t, project.Root, `{"values":[{"key":"STRIPE_API_KEY","value":"sk_live_secret","secret":true}]}`, envOptions{}); err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
	}

	t.Run("ocel env set is refused naming the publisher", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), project.Root, "STRIPE_API_KEY", "by hand", envOptions{}, nil, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), infraPublisher) || !strings.Contains(stderr.String(), "change it in that program") {
			t.Errorf("runEnvSet err = %v; stream = %q, want it refused naming %s and where to change it", err, stderr.String(), infraPublisher)
		}
	})

	t.Run("ocel env rm is refused naming the publisher", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runEnvRemove(context.Background(), newStreamedDependencies(&stderr), project.Root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr)
		if err == nil || !strings.Contains(stderr.String(), infraPublisher) {
			t.Errorf("runEnvRemove err = %v; stream = %q, want it refused naming %s", err, stderr.String(), infraPublisher)
		}
	})

	t.Run("ocel env ls names the publisher as the value's source", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&stderr), project.Root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvList err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), infraPublisher) {
			t.Errorf("ls stdout = %q, want the publisher named", stdout.String())
		}
	})

	t.Run("ocel env get names the publisher", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), project.Root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvGet err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), infraPublisher) {
			t.Errorf("get stdout = %q, want the publisher named", stdout.String())
		}
	})
}
