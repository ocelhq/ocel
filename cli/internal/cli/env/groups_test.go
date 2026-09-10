package env

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const groupedDefinitions = `[
  {"key":"LOG_LEVEL","class":"VARIABLE_CLASS_PLAIN","required":true},
  {"key":"GITHUB_CLIENT_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"group":"github"},
  {"key":"GITHUB_CLIENT_SECRET","class":"VARIABLE_CLASS_PLAIN","required":true,"group":"github"},
  {"key":"GITHUB_SCOPES","class":"VARIABLE_CLASS_PLAIN","group":"github"},
  {"key":"STRIPE_KEY","class":"VARIABLE_CLASS_PLAIN","required":true,"group":"stripe"},
  {"key":"STRIPE_WEBHOOK_SECRET","class":"VARIABLE_CLASS_PLAIN","required":true,"group":"stripe"}
]`

const groupedGroups = `[
  {"key":"github","description":"Sign in with GitHub"},
  {"key":"stripe","required":true}
]`

func setUpGroupedFixture(t *testing.T) string {
	t.Helper()
	return clitest.SetUpEnvGateFixtureWith(t, "[]", envDeclaringRequest(`{"definitions": `+groupedDefinitions+`, "groups": `+groupedGroups+`}`))
}

func seedProductionValue(t *testing.T, key, folder, value string) {
	t.Helper()
	seedFakeValue(t, environmentv1.Tier_TIER_PRODUCTION,
		&envvarsv1.Coordinate{Slug: clitest.FixtureSlug, Key: key, Folder: folder}, value)
}

func TestGroupProgressOnSet(t *testing.T) {
	t.Run("a half-filled group names what is still owed", func(t *testing.T) {
		root := setUpGroupedFixture(t)

		out := envSet(t, root, "GITHUB_CLIENT_ID", "id", envOptions{})
		want := "github: 1 of 2 set. Set together: GITHUB_CLIENT_SECRET"
		if !strings.Contains(out, want) {
			t.Errorf("set stdout = %q, want %q: the count covers what is owed, not the member spelled optional", out, want)
		}
	})

	t.Run("a value set at the base counts for an environment override", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		seedFakeValue(t, environmentv1.Tier_TIER_PREVIEW,
			&envvarsv1.Coordinate{Slug: clitest.FixtureSlug, Key: "GITHUB_CLIENT_SECRET"}, "secret")

		out := envSet(t, root, "GITHUB_CLIENT_ID", "id", envOptions{preview: true, environment: "staging"})
		if strings.Contains(out, "Set together") {
			t.Errorf("set stdout = %q, want the base value to count for staging, as the deploy gate counts it", out)
		}
	})

	t.Run("a filled group says nothing further", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		seedProductionValue(t, "GITHUB_CLIENT_ID", "", "id")

		out := envSet(t, root, "GITHUB_CLIENT_SECRET", "secret", envOptions{})
		if strings.Contains(out, "Set together") {
			t.Errorf("set stdout = %q, want nothing further once every member is set", out)
		}
	})

	t.Run("a value inherited from the project root counts as set", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		seedProductionValue(t, "GITHUB_CLIENT_ID", "", "id")

		out := envSet(t, root, "GITHUB_CLIENT_SECRET", "secret", envOptions{folder: "/web"})
		if strings.Contains(out, "Set together") {
			t.Errorf("set stdout = %q, want the root value to count for /web, as the deploy gate counts it", out)
		}
	})

	t.Run("a warm declaration cache still knows the group", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		envSet(t, root, "GITHUB_CLIENT_ID", "id", envOptions{})

		out := envSet(t, root, "GITHUB_CLIENT_ID", "id2", envOptions{})
		want := "github: 1 of 2 set. Set together: GITHUB_CLIENT_SECRET"
		if !strings.Contains(out, want) {
			t.Errorf("set stdout = %q, want %q", out, want)
		}
	})

	t.Run("every affected group prints, in declaration order", func(t *testing.T) {
		root := setUpGroupedFixture(t)

		var stdout, stderr bytes.Buffer
		err := runEnvSetPairs(context.Background(), clitest.NewDeps(), root, []envSetPair{
			{key: "STRIPE_KEY", value: "sk"},
			{key: "GITHUB_CLIENT_ID", value: "id"},
		}, envOptions{}, nil, &stdout, &stderr)
		if err != nil {
			t.Fatalf("runEnvSetPairs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		github := strings.Index(stdout.String(), "github: 1 of 2 set.")
		stripe := strings.Index(stdout.String(), "stripe: 1 of 2 set.")
		if github < 0 || stripe < 0 {
			t.Fatalf("set stdout = %q, want a line for each half-filled group", stdout.String())
		}
		if github > stripe {
			t.Errorf("set stdout = %q, want the groups in declaration order", stdout.String())
		}
	})
}

func TestGroupProgressOnRm(t *testing.T) {
	t.Run("removing one member leaves the group partial", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		seedProductionValue(t, "GITHUB_CLIENT_ID", "", "id")
		seedProductionValue(t, "GITHUB_CLIENT_SECRET", "", "secret")

		out := envRm(t, root, "GITHUB_CLIENT_ID", envOptions{})
		want := "github: 1 of 2 set. Set together: GITHUB_CLIENT_ID"
		if !strings.Contains(out, want) {
			t.Errorf("rm stdout = %q, want %q", out, want)
		}
	})

	t.Run("emptying an optional group turns it off, and it says nothing", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		seedProductionValue(t, "GITHUB_CLIENT_ID", "", "id")

		out := envRm(t, root, "GITHUB_CLIENT_ID", envOptions{})
		if strings.Contains(out, "Set together") {
			t.Errorf("rm stdout = %q, want an emptied optional group to fall silent: it is off", out)
		}
	})

	t.Run("emptying a required group still owes every member", func(t *testing.T) {
		root := setUpGroupedFixture(t)
		seedProductionValue(t, "STRIPE_KEY", "", "sk")

		out := envRm(t, root, "STRIPE_KEY", envOptions{})
		want := "stripe: 0 of 2 set. Set together: STRIPE_KEY, STRIPE_WEBHOOK_SECRET"
		if !strings.Contains(out, want) {
			t.Errorf("rm stdout = %q, want %q: a required group is owed whatever is set", out, want)
		}
	})
}

func TestEnvRmLeavesTheProjectUnbuiltWhenNothingWasRemoved(t *testing.T) {
	root := setUpGroupedFixture(t)
	log := filepath.Join(t.TempDir(), "discovery.log")
	t.Setenv("OCEL_TEST_DISCOVERY_LOG", log)

	envRm(t, root, "GITHUB_CLIENT_ID", envOptions{})

	if runs := discoveryRuns(t, log); runs != 0 {
		t.Errorf("discovery ran %d times, want none: no value was removed, so nothing needs the declarations", runs)
	}
}

func TestEnvLsGathersGroups(t *testing.T) {
	root := setUpGroupedFixture(t)
	seedProductionValue(t, "LOG_LEVEL", "", "debug")
	seedProductionValue(t, "GITHUB_CLIENT_ID", "", "id")
	seedProductionValue(t, "GITHUB_CLIENT_SECRET", "", "secret")

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), clitest.NewDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if got := strings.Count(out, "set together"); got != 1 {
		t.Errorf("ls stdout = %q, want %q stated once for the group, not once per member", out, "set together")
	}
	header := "github — set together (Sign in with GitHub)"
	if !strings.Contains(out, header) {
		t.Errorf("ls stdout = %q, want the header %q", out, header)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	at := func(prefix string) int {
		for i, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return i
			}
		}
		return -1
	}
	if at("LOG_LEVEL") < 0 {
		t.Errorf("ls stdout = %q, want an ungrouped variable rendered flush left", out)
	}
	if at(header) < at("  GITHUB_CLIENT_ID") && at("  GITHUB_CLIENT_ID") < at("  GITHUB_CLIENT_SECRET") {
		return
	}
	t.Errorf("ls stdout = %q, want the members indented under the group header in declaration order", out)
}
