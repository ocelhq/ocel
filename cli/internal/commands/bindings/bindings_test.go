package bindings

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/pkg/environment"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const fakeBindingPassword = "pw-never-listed-7c31"

func setUpBindingFixture(t *testing.T) string {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
	root := project.Root
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  bindings: { postgres: { orders: "@orders" } },
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
	return root
}

func postgresBindingJSON(name, host string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "fake:postgres:%s",
  "postgres": {"host": %q, "port": 5432, "database": "app", "username": "app", "password": %q}
}`, name, name, host, fakeBindingPassword)
}

func bindingSet(t *testing.T, root, body string, opts bindingsOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root, strings.NewReader(body), opts, &stdout); err != nil {
		t.Fatalf("runBindingsSet err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stderr.String()
}

func bindingList(t *testing.T, root string, opts bindingsOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsList(context.Background(), newStreamedInvocation(&stderr), root, opts, &stdout); err != nil {
		t.Fatalf("runBindingsList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func newStreamedInvocation(stderr io.Writer) commands.Invocation {
	invocation := newTestInvocation()
	clitest.AttachTerminalSink(invocation, stderr)
	return invocation
}

func TestABindingOnStdinThatCannotBeReadIsRefusedNamingWhereWithoutItsValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"malformed JSON", `{"name": "main", "postgres": {"password": hunter2}}`, "line 1"},
		{"a value of the wrong type", `{"name": "main", "postgres": {"port": "hunter2"}}`, "postgres.port"},
		{"a number where a string field wants one", `{"name": "main", "postgres": {"password": 20250101}}`, "postgres.password"},
		{"a field the binding does not have", `{"name": "main", "postgres": {"hunter2": "x"}}`, "postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeBinding(strings.NewReader(tc.input))
			if err == nil {
				t.Fatal("decodeBinding = nil, want a refusal")
			}
			for _, value := range []string{"hunter2", "20250101"} {
				if tc.name != "a field the binding does not have" && strings.Contains(err.Error(), value) {
					t.Errorf("decodeBinding = %q, want it to quote no value", err)
				}
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("decodeBinding = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestBindingsSetPublishesOneRecordUnderItsPublishersName(t *testing.T) {
	t.Run("the record it publishes is what ls shows, and rm takes it away", func(t *testing.T) {
		root := setUpBindingFixture(t)

		if out := bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{}); !strings.Contains(out, "main") {
			t.Errorf("set stdout = %q, want it to name the binding it published", out)
		}

		listed := bindingList(t, root, bindingsOptions{})
		for _, want := range []string{"main", "postgres", "fake:postgres:main", defaultBindingOwner} {
			if !strings.Contains(listed, want) {
				t.Errorf("ls stdout = %q, want it to show %q", listed, want)
			}
		}

		var rm, stderr bytes.Buffer
		if err := runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "main", bindingsOptions{}, &rm); err != nil {
			t.Fatalf("runBindingsRemove err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "Removed main") {
			t.Errorf("rm summary = %q, want it to name the binding it removed", stderr.String())
		}

		if after := bindingList(t, root, bindingsOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the binding gone", after)
		}
	})

	t.Run("refuses to take a name another publisher owns", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		err := runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root,
			strings.NewReader(postgresBindingJSON("main", "other.internal")), bindingsOptions{owner: "cli"}, &stdout)
		if err == nil {
			t.Fatal("runBindingsSet over another publisher's binding err = nil, want a refusal")
		}
		for _, want := range []string{"terraform", "cli"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want the refusal to name %q", stderr.String(), want)
			}
		}
	})

	t.Run("refuses to publish as ocel's own provisioning", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root,
			strings.NewReader(postgresBindingJSON("main", "db.internal")), bindingsOptions{owner: "OCEL"}, &stdout)
		if err == nil {
			t.Fatal("runBindingsSet --owner OCEL err = nil, want a refusal")
		}
		if !strings.Contains(stderr.String(), "OCEL") {
			t.Errorf("stderr = %q, want the refusal to name the publisher it refused", stderr.String())
		}
		if listed := bindingList(t, root, bindingsOptions{}); strings.Contains(listed, "main") {
			t.Errorf("ls = %q, want the refused binding never published", listed)
		}
	})

	t.Run("refuses to publish as the owner of inline bindings' records", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root,
			strings.NewReader(postgresBindingJSON("ocel:postgres.orders", "db.internal")), bindingsOptions{owner: "ocel-config"}, &stdout)
		if err == nil {
			t.Fatal("runBindingsSet --owner ocel-config err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "ocel-config") {
			t.Errorf("err = %v, want it to name the publisher it refused", err)
		}
	})

	t.Run("rm refuses a record an inline binding keeps, pointing at the config", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "ocel:postgres.orders", bindingsOptions{}, &stdout)
		if err == nil {
			t.Fatal("runBindingsRemove ocel:postgres.orders err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "bindings") {
			t.Errorf("err = %v, want it to say the binding is removed from the config", err)
		}
	})

	t.Run("the same publisher bumps the version", func(t *testing.T) {
		root := setUpBindingFixture(t)
		opts := bindingsOptions{owner: "terraform"}
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), opts)
		if out := bindingSet(t, root, postgresBindingJSON("main", "moved.internal"), opts); !strings.Contains(out, "2") {
			t.Errorf("second set stdout = %q, want version 2", out)
		}

		listed := bindingList(t, root, bindingsOptions{})
		if !strings.Contains(listed, "terraform") {
			t.Errorf("ls stdout = %q, want the publisher that owns the name", listed)
		}
	})

	t.Run("rm takes a binding whatever published it", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		if err := runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "main", bindingsOptions{}, &stdout); err != nil {
			t.Fatalf("runBindingsRemove over another publisher's binding err = %v; stderr=%s", err, stderr.String())
		}
		if after := bindingList(t, root, bindingsOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the binding gone", after)
		}
	})

	t.Run("reports nothing to remove", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "never-published", bindingsOptions{}, &stdout); err != nil {
			t.Fatalf("runBindingsRemove err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "never-published") {
			t.Errorf("rm of a binding that was never published = %q, want it to name what it looked for", stderr.String())
		}
	})

	t.Run("refuses stdin that is not a binding", func(t *testing.T) {
		root := setUpBindingFixture(t)

		for name, body := range map[string]string{
			"not JSON at all":            "postgres://db.internal/app",
			"a field no binding has":     `{"name":"main","postgres":{"host":"db.internal"},"nonsense":true}`,
			"nothing at all on stdin":    "",
			"JSON that is not a binding": `["main"]`,
			"a binding with no name":     `{"postgres":{"host":"db.internal"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root, strings.NewReader(body), bindingsOptions{}, &stdout)
				if err == nil {
					t.Fatalf("runBindingsSet(%q) err = nil, want a refusal", body)
				}
				if listed := bindingList(t, root, bindingsOptions{}); strings.Contains(listed, "main") {
					t.Errorf("ls = %q, want the refused binding never published", listed)
				}
			})
		}
	})
}

func TestBindingsListNamesEachRecordWithoutItsValues(t *testing.T) {
	t.Run("never prints a property value", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{})

		for name, run := range map[string]func() string{
			"human": func() string { return bindingList(t, root, bindingsOptions{}) },
			"json": func() string {
				useJSONOutput(t)
				return bindingList(t, root, bindingsOptions{})
			},
		} {
			t.Run(name, func(t *testing.T) {
				out := run()
				for _, secret := range []string{fakeBindingPassword, "db.internal", "5432"} {
					if strings.Contains(out, secret) {
						t.Errorf("ls stdout = %q, want no property value printed (found %q)", out, secret)
					}
				}
			})
		}
	})

	t.Run("reports an empty listing", func(t *testing.T) {
		root := setUpBindingFixture(t)
		if out := bindingList(t, root, bindingsOptions{}); !strings.Contains(out, "ocel bindings set") {
			t.Errorf("ls with nothing published = %q, want it to name the command that publishes one", out)
		}
	})
}

func customBindingJSON(name string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "terraform:module.network",
  "custom": {"zones": ["zone-a", "zone-b"], "peers": ["peer-1"], "port": 5432}
}`, name)
}

func bindingGenerate(t *testing.T, root string, opts bindingsOptions) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsGenerate(context.Background(), newStreamedInvocation(&stderr), root, opts, &stdout); err != nil {
		t.Fatalf("runBindingsGenerate err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	written, err := os.ReadFile(filepath.Join(root, bindingTypesFileName))
	if err != nil {
		t.Fatalf("read the generated types: %v", err)
	}
	return stderr.String(), string(written)
}

func renderedPropertyTypes(t *testing.T, written string) []string {
	t.Helper()
	_, declared, ok := strings.Cut(written, "interface Bindings {\n")
	if !ok {
		t.Fatalf("generated file =\n%s\nnames no Bindings interface", written)
	}
	body, _, ok := strings.Cut(declared, "\n  }\n")
	if !ok {
		t.Fatalf("generated file =\n%s\nnever closes the Bindings interface", written)
	}

	var types []string
	for _, line := range strings.Split(body, "\n") {
		_, properties, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}
		if properties == "{};" || properties == "{" {
			continue
		}
		properties = strings.TrimSuffix(strings.TrimPrefix(properties, "{ "), " };")
		for _, property := range strings.Split(properties, "; ") {
			_, rendered, ok := strings.Cut(property, ": ")
			if !ok {
				t.Fatalf("generated file =\n%s\nwrote %q, which names no type", written, property)
			}
			types = append(types, rendered)
		}
	}
	return types
}

func TestBindingsGenerateWritesTheShapeOfEveryPublishedRecord(t *testing.T) {
	t.Run("writes the shape of every published record and none of its values", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("orders", "db.internal"), bindingsOptions{})
		bindingSet(t, root, customBindingJSON("network"), bindingsOptions{owner: "terraform"})

		out, written := bindingGenerate(t, root, bindingsOptions{})

		for _, want := range []string{
			"// generated by `ocel bindings generate` from production; do not edit",
			`declare module "@ocel/transforms"`,
			"      network: { peers: string[]; port: number; zones: string[] };",
			"      orders: { host: string; port: number; database: string; username: string; password: string; url: string; tlsMode: string; tlsCa: string };",
		} {
			if !strings.Contains(written, want) {
				t.Errorf("generated file =\n%s\nwant it to contain %q", written, want)
			}
		}
		types := renderedPropertyTypes(t, written)
		if len(types) != 11 {
			t.Fatalf("generated file =\n%s\nrenders %d properties, want the eleven the two records have", written, len(types))
		}
		for _, rendered := range types {
			if !slices.Contains([]string{"string", "number", "boolean", "unknown", "Record<string, unknown>"}, strings.TrimSuffix(rendered, "[]")) {
				t.Errorf("generated file =\n%s\nwrote %q for a property; a shape says how a property reads, never what it contains", written, rendered)
			}
		}
		if !strings.Contains(out, bindingTypesFileName) {
			t.Errorf("generate stdout = %q, want it to name the file it wrote", out)
		}
	})

	t.Run("addresses the coordinate its flags name", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("orders", "db.internal"), bindingsOptions{})

		bindingSet(t, root, customBindingJSON("network"), bindingsOptions{preview: true, environment: "staging", owner: "terraform"})

		_, written := bindingGenerate(t, root, bindingsOptions{preview: true, environment: "staging"})
		if !strings.Contains(written, "from the preview environment staging;") {
			t.Errorf("generated file =\n%s\nwant the header to name the coordinate it read", written)
		}
		if !strings.Contains(written, "network:") || strings.Contains(written, "orders:") {
			t.Errorf("generated file =\n%s\nwant only what that coordinate has", written)
		}
	})

	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsGenerate(context.Background(), newStreamedInvocation(&stderr), root, bindingsOptions{environment: "staging"}, &stdout)
		if err == nil {
			t.Fatal("runBindingsGenerate --environment against production err = nil, want a refusal")
		}
		if _, statErr := os.Stat(filepath.Join(root, bindingTypesFileName)); statErr == nil {
			t.Error("a refused generate wrote the file anyway")
		}
	})

	t.Run("names no record when nothing is published", func(t *testing.T) {
		root := setUpBindingFixture(t)

		out, written := bindingGenerate(t, root, bindingsOptions{})
		if !strings.Contains(written, "interface Bindings {\n  }\n") {
			t.Errorf("generated file =\n%s\nwant an interface naming no record", written)
		}
		if !strings.Contains(written, "interface BindingsGenerated {\n    generated: true;\n  }") {
			t.Errorf("generated file =\n%s\nwant it to mark itself generated, so an empty coordinate still closes every name", written)
		}
		if !strings.Contains(out, "Nothing is published") || !strings.Contains(out, "no binding name open") {
			t.Errorf("generate summary = %q, want it to say nothing was published and that no name stays open", out)
		}
	})
}

func TestABindingBelongsToTheBootstrapItsFlagsAddress(t *testing.T) {
	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		for name, err := range map[string]error{
			"set": runBindingsSet(context.Background(), newStreamedInvocation(&stderr), root, strings.NewReader(postgresBindingJSON("main", "db.internal")), bindingsOptions{environment: "staging"}, &stdout),
			"rm":  runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "main", bindingsOptions{environment: "staging"}, &stdout),
			"ls":  runBindingsList(context.Background(), newStreamedInvocation(&stderr), root, bindingsOptions{environment: "staging"}, &stdout),
		} {
			if err == nil {
				t.Errorf("`ocel bindings %s --environment` against production err = nil, want a refusal", name)
				continue
			}
			if !strings.Contains(err.Error(), "--preview") {
				t.Errorf("`ocel bindings %s` err = %v, want it to name the flag that selects the bootstrap overrides live on", name, err)
			}
		}
	})

	t.Run("a preview binding is not a production binding", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "prod.internal"), bindingsOptions{})

		if out := bindingList(t, root, bindingsOptions{preview: true}); strings.Contains(out, "main") {
			t.Errorf("preview ls = %q, want no production binding listed", out)
		}

		bindingSet(t, root, postgresBindingJSON("staged", "staging.internal"), bindingsOptions{preview: true, environment: "staging"})
		if out := bindingList(t, root, bindingsOptions{preview: true, environment: "staging"}); !strings.Contains(out, "staged") {
			t.Errorf("ls --preview --environment staging = %q, want the binding that environment has", out)
		}

		if out := bindingList(t, root, bindingsOptions{}); strings.Contains(out, "staged") {
			t.Errorf("production ls = %q, want no preview binding listed", out)
		}
	})
}

func TestRunBindingsJSONOutput(t *testing.T) {
	root := setUpBindingFixture(t)
	useJSONOutput(t)

	var published, setLog bytes.Buffer
	if err := runBindingsSet(context.Background(), newStreamedInvocation(&setLog), root, strings.NewReader(postgresBindingJSON("main", "db.internal")), bindingsOptions{}, &published); err != nil {
		t.Fatalf("runBindingsSet err = %v; stderr=%s", err, setLog.String())
	}
	set := clitest.DecodeJSON(t, published.String())
	if set["name"] != "main" || set["version"] != float64(1) {
		t.Errorf("set json = %v, want the name and version it published", set)
	}

	listed := clitest.DecodeJSON(t, bindingList(t, root, bindingsOptions{}))
	bindings, ok := listed["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("ls json = %v, want one binding", listed)
	}
	binding, _ := bindings[0].(map[string]any)
	for field, want := range map[string]any{"name": "main", "type": "postgres", "source": "fake:postgres:main", "owner": defaultBindingOwner, "version": float64(1)} {
		if binding[field] != want {
			t.Errorf("ls json binding %s = %v, want %v", field, binding[field], want)
		}
	}

	var rm, stderr bytes.Buffer
	if err := runBindingsRemove(context.Background(), newStreamedInvocation(&stderr), root, "main", bindingsOptions{}, &rm); err != nil {
		t.Fatalf("runBindingsRemove err = %v; stderr=%s", err, stderr.String())
	}
	removed := clitest.DecodeJSON(t, rm.String())
	if removed["name"] != "main" || removed["removed"] != true {
		t.Errorf("rm json = %v, want the name it removed and that it was there", removed)
	}
}

func TestEveryBindingsSubcommandAddressesABootstrapAndOnlySetTakesAnOwner(t *testing.T) {
	t.Parallel()

	subcommand := func(t *testing.T, name string) *cobra.Command {
		t.Helper()
		cmd, _, err := NewCommand(clitest.NewInvocation()).Find([]string{name})
		if err != nil || cmd.Name() != name {
			t.Fatalf("`ocel bindings %s` is not a subcommand: %v", name, err)
		}
		return cmd
	}

	t.Run("every subcommand addresses a bootstrap", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"set", "rm", "ls", "generate"} {
			c := subcommand(t, name)
			for _, flag := range []string{"preview", "environment"} {
				if c.Flags().Lookup(flag) == nil {
					t.Errorf("`ocel bindings %s` cannot address --%s", c.Name(), flag)
				}
			}
		}
	})

	t.Run("only set publishes under a name", func(t *testing.T) {
		t.Parallel()

		owner := subcommand(t, "set").Flags().Lookup("owner")
		if owner == nil {
			t.Fatal("`ocel bindings set` registers no --owner; the publisher is what keeps one from taking another's binding")
		}
		if owner.DefValue != defaultBindingOwner {
			t.Errorf("--owner defaults to %q, want %q", owner.DefValue, defaultBindingOwner)
		}
		for _, name := range []string{"rm", "ls", "generate"} {
			if subcommand(t, name).Flags().Lookup("owner") != nil {
				t.Errorf("`ocel bindings %s` registers --owner; only publishing takes a name", name)
			}
		}
	})

	t.Run("`ocel bindings generate` says it runs the provider", func(t *testing.T) {
		t.Parallel()

		if generate := subcommand(t, "generate"); !strings.Contains(generate.Long, "provider") {
			t.Errorf("`ocel bindings generate` long = %q, want it to say it runs the provider", generate.Long)
		}
	})
}

func TestListingBindingsAsJSONSaysWhoItActsAsOnItsRunAndPrintsOneJSONDocumentAloneOnStdout(t *testing.T) {
	root := setUpBindingFixture(t)
	bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{})
	useJSONOutput(t)
	invocation := newTestInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runBindingsList(context.Background(), invocation, root, bindingsOptions{}, &stdout); err != nil {
		t.Fatalf("runBindingsList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stderr.String())
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < 0 || evs[identity].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("the listing never said who it acts as in the check phase: %s", stderr.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() {
		t.Errorf("result = %v, want the listing's run to succeed", result)
	}
	if listed := clitest.DecodeJSON(t, stdout.String()); listed["bindings"] == nil {
		t.Errorf("stdout = %q, want the listing as one JSON document", stdout.String())
	}
}
