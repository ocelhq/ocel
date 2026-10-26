package env

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
)

func requireNamedUnder(t *testing.T, transcript, phase, ended string) {
	t.Helper()
	for line := range strings.Lines(transcript) {
		if strings.Contains(line, ended) {
			if !strings.Contains(line, "["+phase+"]") {
				t.Errorf("%q was reported as %q, want it under [%s]", ended, line, phase)
			}
			return
		}
	}
	t.Errorf("transcript = %q, want %q reported", transcript, ended)
}

func TestEveryEnvCommandNamesItsWorkWithTheValues(t *testing.T) {
	type command func(t *testing.T, root string, stderr *bytes.Buffer) error
	cases := []struct {
		name   string
		run    command
		phase  string
		ended  string
		seeded bool
	}{
		{
			name: "set",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvSetPairs(context.Background(), newStreamedDependencies(stderr), root, []envSetPair{{key: "LOG_LEVEL", value: "debug"}, {key: "POSTHOG_ID", value: "ph"}}, envOptions{}, nil, &bytes.Buffer{}, stderr)
			},
			phase: "provision",
			ended: "Updated the production values of LOG_LEVEL and POSTHOG_ID",
		},
		{
			name: "get",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvGet(context.Background(), newStreamedDependencies(stderr), root, "LOG_LEVEL", envOptions{folder: "/web"}, &bytes.Buffer{}, stderr)
			},
			phase:  "check",
			ended:  "Read the production value of LOG_LEVEL in /web",
			seeded: true,
		},
		{
			name: "rm",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvRemove(context.Background(), newStreamedDependencies(stderr), root, "LOG_LEVEL", envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase:  "destroy",
			ended:  "Removed the production value of LOG_LEVEL",
			seeded: true,
		},
		{
			name: "ls",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvList(context.Background(), newStreamedDependencies(stderr), root, envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase: "check",
			ended: "Read the production values",
		},
		{
			name: "history",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvHistory(context.Background(), newStreamedDependencies(stderr), root, "LOG_LEVEL", envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase:  "check",
			ended:  "Read the history of the production value of LOG_LEVEL",
			seeded: true,
		},
		{
			name: "ref",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvRef(context.Background(), newStreamedDependencies(stderr), root, "POSTHOG_ID", envOptions{}, envRefOptions{key: "LOG_LEVEL"}, &bytes.Buffer{}, stderr)
			},
			phase:  "provision",
			ended:  "Updated the production value of POSTHOG_ID to read test-app/LOG_LEVEL",
			seeded: true,
		},
		{
			name: "refs",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvRefs(context.Background(), newStreamedDependencies(stderr), root, "LOG_LEVEL", envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase: "check",
			ended: "Read the references to the production value of LOG_LEVEL",
		},
		{
			name: "publish",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvPublish(context.Background(), newStreamedDependencies(stderr), root, infraPublisher, strings.NewReader(`{"values":[{"key":"LOG_LEVEL","value":"debug"}]}`), envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase: "provision",
			ended: "Updated the production values " + infraPublisher + " publishes",
		},
		{
			name: "unpublish",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvUnpublish(context.Background(), newStreamedDependencies(stderr), root, infraPublisher, strings.NewReader(`{"values":[{"key":"LOG_LEVEL","version":"1"}]}`), envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase: "destroy",
			ended: "Removed the production values " + infraPublisher + " published",
		},
		{
			name: "source",
			run: func(t *testing.T, root string, stderr *bytes.Buffer) error {
				return runEnvSource(context.Background(), newStreamedDependencies(stderr), root, envOptions{}, &bytes.Buffer{}, stderr)
			},
			phase: "check",
			ended: "Read the env source of the production values",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := setUpEnvFixture(t).Root
			if c.seeded {
				envSet(t, root, "LOG_LEVEL", "debug", envOptions{})
				envSet(t, root, "LOG_LEVEL", "debug", envOptions{folder: "/web"})
			}
			var stderr bytes.Buffer
			if err := c.run(t, root, &stderr); err != nil {
				t.Fatalf("err = %v; stderr=%s", err, stderr.String())
			}
			requireNamedUnder(t, stderr.String(), c.phase, c.ended)
		})
	}

	t.Run("sync", func(t *testing.T) {
		project := setUpEnvSourceFixture(t)
		source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk", "API_TOKEN": "t"})
		setCredentials(t, project.Root)
		registerEnvSource(t, project, environment.TierProduction, source.at(envsource.WriteNever))

		var stderr bytes.Buffer
		if err := runEnvSync(context.Background(), newStreamedDependencies(&stderr), project.Root, envOptions{}, &bytes.Buffer{}, &stderr); err != nil {
			t.Fatalf("runEnvSync err = %v; stderr=%s", err, stderr.String())
		}
		requireNamedUnder(t, stderr.String(), "provision", "Updated the production values from their env source")
	})

	t.Run("a group member's set reads what its group still lacks", func(t *testing.T) {
		root := setUpGroupedFixture(t).Root
		var stderr bytes.Buffer
		if err := runEnvSet(context.Background(), newStreamedDependencies(&stderr), root, "GITHUB_CLIENT_ID", "id", envOptions{}, nil, &bytes.Buffer{}, &stderr); err != nil {
			t.Fatalf("runEnvSet err = %v; stderr=%s", err, stderr.String())
		}
		requireNamedUnder(t, stderr.String(), "check", "Read the production values")
	})

	t.Run("a read whose declarations are cached still names their preparation", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "LOG_LEVEL", "debug", envOptions{})
		var stderr bytes.Buffer
		if err := runEnvGet(context.Background(), newStreamedDependencies(&stderr), root, "LOG_LEVEL", envOptions{}, &bytes.Buffer{}, &stderr); err != nil {
			t.Fatalf("runEnvGet err = %v; stderr=%s", err, stderr.String())
		}
		requireNamedUnder(t, stderr.String(), "build", "Collected the variables this project declares")
	})
}
