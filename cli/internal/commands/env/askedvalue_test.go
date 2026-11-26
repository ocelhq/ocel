package env

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type answeringPrompt struct {
	answers map[string]string
	asked   []string
}

func (p *answeringPrompt) Input(_ context.Context, title, description string) (string, bool, error) {
	return p.answer("input", title, description)
}

func (p *answeringPrompt) Secret(_ context.Context, title, description string) (string, bool, error) {
	return p.answer("secret", title, description)
}

func (p *answeringPrompt) answer(kind, title, description string) (string, bool, error) {
	p.asked = append(p.asked, strings.TrimSpace(kind+" "+title+" "+description))
	value, ok := p.answers[title]
	return value, ok, nil
}

func askingDependencies(prompt commands.ValuePrompt) Dependencies {
	dependencies := newTestDependencies()
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dependencies.Values = prompt
	return dependencies
}

func asked(key string) envSetPair {
	return envSetPair{key: key, asked: true}
}

func TestABareKeyIsReadAsOneToAskForOnlyWhereSomeoneCanAnswer(t *testing.T) {
	pairs, err := parseEnvSetPairs([]string{"LOG_LEVEL=debug", "STRIPE_API_KEY"}, true)
	if err != nil {
		t.Fatalf("parseEnvSetPairs() = %v", err)
	}
	want := []envSetPair{{key: "LOG_LEVEL", value: "debug"}, {key: "STRIPE_API_KEY", asked: true, arg: 2}}
	if !slices.Equal(pairs, want) {
		t.Errorf("parseEnvSetPairs() = %#v, want %#v", pairs, want)
	}

	if _, err := parseEnvSetPairs([]string{"STRIPE_API_KEY"}, false); err == nil {
		t.Error("parseEnvSetPairs(KEY) with nobody to answer = nil, want today's refusal")
	}
	if _, err := parseEnvSetPairs([]string{"=hunter2"}, true); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("parseEnvSetPairs(=VALUE) = %v, want a refusal that echoes no value", err)
	}
}

func TestEnvSetAsksForTheValueOfABareKeyHidingWhatASecretIsTyped(t *testing.T) {
	project := setUpEnvFixture(t)
	prompt := &answeringPrompt{answers: map[string]string{"STRIPE_API_KEY": "sk_live_typed", "LOG_LEVEL": "debug"}}

	var stdout, stderr bytes.Buffer
	err := runEnvSetPairs(context.Background(), askingDependencies(prompt), project.Root, []envSetPair{asked("STRIPE_API_KEY"), asked("LOG_LEVEL")}, envOptions{}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runEnvSetPairs() = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if want := []string{"secret STRIPE_API_KEY", "input LOG_LEVEL"}; !slices.Equal(prompt.asked, want) {
		t.Errorf("asked %q, want %q: a secret is typed hidden, a plain value in the open", prompt.asked, want)
	}
	for key, want := range map[string]string{"STRIPE_API_KEY": "sk_live_typed", "LOG_LEVEL": "debug"} {
		if got := envGet(t, project.Root, key, envOptions{reveal: true, yes: true}); strings.TrimSpace(got) != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestEnvSetAsksWithTheDescriptionTheDeclarationGives(t *testing.T) {
	project := setUpDeclaringProject(t, envDeclaringScript(`[{"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true,"description":"How much the app logs"}]`))
	prompt := &answeringPrompt{answers: map[string]string{"LOG_LEVEL": "debug"}}

	var stdout, stderr bytes.Buffer
	if err := runEnvSetPairs(context.Background(), askingDependencies(prompt), project.Root, []envSetPair{asked("LOG_LEVEL")}, envOptions{}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvSetPairs() = %v; stderr=%s", err, stderr.String())
	}
	if want := []string{"input LOG_LEVEL How much the app logs"}; !slices.Equal(prompt.asked, want) {
		t.Errorf("asked %q, want %q", prompt.asked, want)
	}
}

func TestABareArgumentNoDeclarationNamesIsRefusedByPositionWithoutAskingOrEchoingIt(t *testing.T) {
	project := setUpEnvFixture(t)
	prompt := &answeringPrompt{}

	var stdout, stderr bytes.Buffer
	err := runEnvSetPairs(context.Background(), askingDependencies(prompt), project.Root, []envSetPair{{key: "LOG_LEVEL", value: "debug"}, {key: "sk_live_pasted", asked: true, arg: 2}}, envOptions{}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSetPairs() = nil, want the undeclared bare argument refused")
	}
	if strings.Contains(err.Error()+stderr.String()+stdout.String(), "sk_live_pasted") {
		t.Errorf("err = %q, stderr = %q: echoes an argument that may be a value", err, stderr.String())
	}
	if !strings.Contains(err.Error(), "argument 2") {
		t.Errorf("err = %q, want the argument named by position", err)
	}
	if len(prompt.asked) != 0 {
		t.Errorf("asked %q, want nothing asked", prompt.asked)
	}
	if sent := clitest.RequestsTo[*variablestorev1.SetValueRequest](t, project.Requests, variablestorev1connect.VariableStoreServiceSetValueProcedure); len(sent) != 0 {
		t.Errorf("the CLI sent %d writes, want none", len(sent))
	}
}

func TestAnAskedValueLeftEmptyWritesNothing(t *testing.T) {
	for name, prompt := range map[string]*answeringPrompt{
		"left empty":     {answers: map[string]string{"LOG_LEVEL": "debug", "API_TOKEN": ""}},
		"never answered": {answers: map[string]string{"LOG_LEVEL": "debug"}},
	} {
		t.Run(name, func(t *testing.T) {
			project := setUpEnvFixture(t)

			var stdout, stderr bytes.Buffer
			err := runEnvSetPairs(context.Background(), askingDependencies(prompt), project.Root, []envSetPair{asked("LOG_LEVEL"), asked("API_TOKEN")}, envOptions{}, nil, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "API_TOKEN=") {
				t.Fatalf("runEnvSetPairs() = %v, want a refusal saying how to store an empty value on purpose", err)
			}
			if sent := clitest.RequestsTo[*variablestorev1.SetValueRequest](t, project.Requests, variablestorev1connect.VariableStoreServiceSetValueProcedure); len(sent) != 0 {
				t.Errorf("the CLI sent %d writes, want none: every value is asked before any is written", len(sent))
			}
		})
	}
}

func TestEnvSetRefusesAValueTheEnvSourceOwnsBeforeWritingOrAskingForAny(t *testing.T) {
	t.Run("a pair set alongside it is not written", func(t *testing.T) {
		project, _ := syncedEnvSourceFixture(t, envsource.WriteNever)

		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), newStreamedDependencies(&stderr), project.Root, []envSetPair{
			{key: "INFISICAL_CLIENT_SECRET", value: "rotated"},
			{key: "STRIPE_API_KEY", value: "by-hand"},
		}, envOptions{}, nil, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "infisical:p-1/prod") || !strings.Contains(err.Error(), "nothing was written") {
			t.Fatalf("runEnvSetPairs() = %v, want STRIPE_API_KEY refused naming its env source, with nothing written", err)
		}
		if got := envGet(t, project.Root, "INFISICAL_CLIENT_SECRET", envOptions{reveal: true, yes: true}); strings.TrimSpace(got) != "client-secret" {
			t.Errorf("INFISICAL_CLIENT_SECRET = %q, want it left as it was", got)
		}
	})

	t.Run("its value is never asked for", func(t *testing.T) {
		project, _ := syncedEnvSourceFixture(t, envsource.WriteNever)
		prompt := &answeringPrompt{answers: map[string]string{"STRIPE_API_KEY": "by-hand"}}

		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), askingDependencies(prompt), project.Root, []envSetPair{asked("STRIPE_API_KEY")}, envOptions{}, nil, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "infisical:p-1/prod") {
			t.Fatalf("runEnvSetPairs() = %v, want STRIPE_API_KEY refused naming its env source", err)
		}
		if len(prompt.asked) != 0 {
			t.Errorf("asked %q, want a value the env source owns never typed in", prompt.asked)
		}
	})

	t.Run("a preview environment's own value is still ocel's to store", func(t *testing.T) {
		project := setUpEnvSourceFixture(t)
		clitest.Bootstrap(t, project.Provider, environment.TierPreview)
		source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk"})
		registerEnvSource(t, project, environment.TierPreview, source.at(envsource.WriteNever))
		seedEnvironment(t, project, "staging")

		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), newStreamedDependencies(&stderr), project.Root, []envSetPair{{key: "STRIPE_API_KEY", value: "sk_staging"}}, envOptions{preview: true, environment: "staging"}, nil, &stdout, &stderr)
		if err != nil {
			t.Fatalf("runEnvSetPairs() = %v, want a value for one preview environment stored by ocel; stderr=%s", err, stderr.String())
		}
		at := variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_API_KEY"}, Environment: "staging"}
		if _, err := valuesOf(project).Get(context.Background(), variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierPreview}, at, false); err != nil {
			t.Errorf("the staging value was not stored: %v", err)
		}
	})
}
