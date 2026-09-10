package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

const fakeBindingPassword = "pw-never-listed-7c31"

func setUpBindingFixture(t *testing.T) string {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeBindingsStoreEnvVar, filepath.Join(t.TempDir(), "bindings.json"))
	return root
}

func postgresBindingJSON(name, host string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "aws:rds:%s",
  "postgres": {"host": %q, "port": 5432, "database": "app", "username": "app", "password": %q}
}`, name, name, host, fakeBindingPassword)
}

func bindingSet(t *testing.T, root, body string, opts bindingsOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsSet(context.Background(), newDeps(), root, strings.NewReader(body), opts, &stdout, &stderr); err != nil {
		t.Fatalf("runBindingsSet err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bindingLs(t *testing.T, root string, opts bindingsOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsLs(context.Background(), newDeps(), root, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runBindingsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func asJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("stdout = %q is not JSON: %v", out, err)
	}
	return rec
}

func jsonOutput(t *testing.T) {
	t.Helper()
	orig := logFormatFlag
	t.Cleanup(func() { logFormatFlag = orig })
	logFormatFlag = string(runui.FormatJSON)
}

func TestRunBindingsSet(t *testing.T) {
	t.Run("the record it publishes is what ls shows, and rm takes it away", func(t *testing.T) {
		root := setUpBindingFixture(t)

		if out := bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{}); !strings.Contains(out, "main") {
			t.Errorf("set stdout = %q, want it to name the binding it published", out)
		}

		listed := bindingLs(t, root, bindingsOptions{})
		for _, want := range []string{"main", "postgres", "aws:rds:main", defaultBindingOwner} {
			if !strings.Contains(listed, want) {
				t.Errorf("ls stdout = %q, want it to show %q", listed, want)
			}
		}

		var rm, stderr bytes.Buffer
		if err := runBindingsRm(context.Background(), newDeps(), root, "main", bindingsOptions{}, &rm, &stderr); err != nil {
			t.Fatalf("runBindingsRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(rm.String(), "main") {
			t.Errorf("rm stdout = %q, want it to name the binding it removed", rm.String())
		}

		if after := bindingLs(t, root, bindingsOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the binding gone", after)
		}
	})

	t.Run("refuses to take a name another publisher holds", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		err := runBindingsSet(context.Background(), newDeps(), root,
			strings.NewReader(postgresBindingJSON("main", "other.internal")), bindingsOptions{owner: "cli"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runBindingsSet over another publisher's binding err = nil, want a refusal")
		}
		for _, want := range []string{"terraform", "cli"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("refuses to publish as ocel's own provisioning", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsSet(context.Background(), newDeps(), root,
			strings.NewReader(postgresBindingJSON("main", "db.internal")), bindingsOptions{owner: "OCEL"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runBindingsSet --owner OCEL err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "OCEL") {
			t.Errorf("err = %v, want it to name the publisher it refused", err)
		}
		if listed := bindingLs(t, root, bindingsOptions{}); strings.Contains(listed, "main") {
			t.Errorf("ls = %q, want the refused binding never published", listed)
		}
	})

	t.Run("the same publisher bumps the version", func(t *testing.T) {
		root := setUpBindingFixture(t)
		opts := bindingsOptions{owner: "terraform"}
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), opts)
		if out := bindingSet(t, root, postgresBindingJSON("main", "moved.internal"), opts); !strings.Contains(out, "2") {
			t.Errorf("second set stdout = %q, want version 2", out)
		}

		listed := bindingLs(t, root, bindingsOptions{})
		if !strings.Contains(listed, "terraform") {
			t.Errorf("ls stdout = %q, want the publisher that holds the name", listed)
		}
	})

	t.Run("rm takes a binding whatever published it", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		if err := runBindingsRm(context.Background(), newDeps(), root, "main", bindingsOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runBindingsRm over another publisher's binding err = %v; stderr=%s", err, stderr.String())
		}
		if after := bindingLs(t, root, bindingsOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the binding gone", after)
		}
	})

	t.Run("reports nothing to remove", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runBindingsRm(context.Background(), newDeps(), root, "never-published", bindingsOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runBindingsRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "never-published") {
			t.Errorf("rm of a binding that was never published = %q, want it to name what it looked for", stdout.String())
		}
	})

	t.Run("refuses stdin that is not a binding", func(t *testing.T) {
		root := setUpBindingFixture(t)

		for name, body := range map[string]string{
			"not JSON at all":            "postgres://db.internal/app",
			"a field no binding carries": `{"name":"main","postgres":{"host":"db.internal"},"nonsense":true}`,
			"nothing at all on stdin":    "",
			"JSON that is not a binding": `["main"]`,
			"a binding with no name":     `{"postgres":{"host":"db.internal"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runBindingsSet(context.Background(), newDeps(), root, strings.NewReader(body), bindingsOptions{}, &stdout, &stderr)
				if err == nil {
					t.Fatalf("runBindingsSet(%q) err = nil, want a refusal", body)
				}
				if listed := bindingLs(t, root, bindingsOptions{}); strings.Contains(listed, "main") {
					t.Errorf("ls = %q, want the refused binding never published", listed)
				}
			})
		}
	})
}

func TestRunBindingsLs(t *testing.T) {
	t.Run("never prints a property value", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{})

		for name, run := range map[string]func() string{
			"human": func() string { return bindingLs(t, root, bindingsOptions{}) },
			"json": func() string {
				jsonOutput(t)
				return bindingLs(t, root, bindingsOptions{})
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
		if out := bindingLs(t, root, bindingsOptions{}); !strings.Contains(out, "ocel bindings set") {
			t.Errorf("ls with nothing published = %q, want it to name the command that publishes one", out)
		}
	})
}

func customBindingJSON(name string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "terraform:module.network",
  "custom": {"subnetIds": ["subnet-0a1", "subnet-0b2"], "securityGroupIds": ["sg-1"], "port": 5432}
}`, name)
}

func bindingGenerate(t *testing.T, root string, opts bindingsOptions) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runBindingsGenerate(context.Background(), newDeps(), root, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runBindingsGenerate err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	written, err := os.ReadFile(filepath.Join(root, bindingTypesFileName))
	if err != nil {
		t.Fatalf("read the generated types: %v", err)
	}
	return stdout.String(), string(written)
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
		if properties == "{};" {
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

func TestRunBindingsGenerate(t *testing.T) {
	t.Run("writes the shape of every published record and none of its values", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("orders", "db.internal"), bindingsOptions{})
		bindingSet(t, root, customBindingJSON("network"), bindingsOptions{owner: "terraform"})

		out, written := bindingGenerate(t, root, bindingsOptions{})

		for _, want := range []string{
			"// generated by `ocel binding generate` from production; do not edit",
			`declare module "ocel/providers/aws/transform"`,
			"network: { port: number; securityGroupIds: string[]; subnetIds: string[] };",
			"orders: { host: string; port: number; database: string; username: string; password: string };",
		} {
			if !strings.Contains(written, want) {
				t.Errorf("generated file =\n%s\nwant it to hold %q", written, want)
			}
		}
		types := renderedPropertyTypes(t, written)
		if len(types) != 8 {
			t.Fatalf("generated file =\n%s\nrenders %d properties, want the eight the two records carry", written, len(types))
		}
		for _, rendered := range types {
			if !slices.Contains([]string{"string", "number", "boolean", "unknown", "Record<string, unknown>"}, strings.TrimSuffix(rendered, "[]")) {
				t.Errorf("generated file =\n%s\nwrote %q for a property; a shape says how a property reads, never what it holds", written, rendered)
			}
		}
		if !strings.Contains(out, bindingTypesFileName) {
			t.Errorf("generate stdout = %q, want it to name the file it wrote", out)
		}
	})

	t.Run("addresses the coordinate its flags name", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("orders", "db.internal"), bindingsOptions{})

		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		bindingSet(t, root, customBindingJSON("network"), bindingsOptions{preview: true, environment: "staging", owner: "terraform"})

		_, written := bindingGenerate(t, root, bindingsOptions{preview: true, environment: "staging"})
		if !strings.Contains(written, "from the preview environment staging;") {
			t.Errorf("generated file =\n%s\nwant the header to name the coordinate it read", written)
		}
		if !strings.Contains(written, "network:") || strings.Contains(written, "orders:") {
			t.Errorf("generated file =\n%s\nwant only what that coordinate holds", written)
		}
	})

	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		err := runBindingsGenerate(context.Background(), newDeps(), root, bindingsOptions{environment: "staging"}, &stdout, &stderr)
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
			t.Errorf("generate stdout = %q, want it to say nothing was published and that no name stays open", out)
		}
	})
}

func TestBindingBootstrap(t *testing.T) {
	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpBindingFixture(t)

		var stdout, stderr bytes.Buffer
		for name, err := range map[string]error{
			"set": runBindingsSet(context.Background(), newDeps(), root, strings.NewReader(postgresBindingJSON("main", "db.internal")), bindingsOptions{environment: "staging"}, &stdout, &stderr),
			"rm":  runBindingsRm(context.Background(), newDeps(), root, "main", bindingsOptions{environment: "staging"}, &stdout, &stderr),
			"ls":  runBindingsLs(context.Background(), newDeps(), root, bindingsOptions{environment: "staging"}, &stdout, &stderr),
		} {
			if err == nil {
				t.Errorf("`ocel binding %s --environment` against production err = nil, want a refusal", name)
				continue
			}
			if !strings.Contains(err.Error(), "--preview") {
				t.Errorf("`ocel binding %s` err = %v, want it to name the flag that selects the bootstrap overrides live on", name, err)
			}
		}
	})

	t.Run("a preview binding is not a production binding", func(t *testing.T) {
		root := setUpBindingFixture(t)
		bindingSet(t, root, postgresBindingJSON("main", "prod.internal"), bindingsOptions{})

		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		if out := bindingLs(t, root, bindingsOptions{preview: true}); strings.Contains(out, "main") {
			t.Errorf("preview ls = %q, want no production binding listed", out)
		}

		bindingSet(t, root, postgresBindingJSON("staged", "staging.internal"), bindingsOptions{preview: true, environment: "staging"})
		if out := bindingLs(t, root, bindingsOptions{preview: true, environment: "staging"}); !strings.Contains(out, "staged") {
			t.Errorf("ls --preview --environment staging = %q, want the binding that environment holds", out)
		}

		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		if out := bindingLs(t, root, bindingsOptions{}); strings.Contains(out, "staged") {
			t.Errorf("production ls = %q, want no preview binding listed", out)
		}
	})
}

func TestRunBindingsJSONOutput(t *testing.T) {
	root := setUpBindingFixture(t)
	jsonOutput(t)

	set := asJSON(t, bindingSet(t, root, postgresBindingJSON("main", "db.internal"), bindingsOptions{}))
	if set["name"] != "main" || set["version"] != float64(1) {
		t.Errorf("set json = %v, want the name and version it published", set)
	}

	listed := asJSON(t, bindingLs(t, root, bindingsOptions{}))
	bindings, ok := listed["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("ls json = %v, want one binding", listed)
	}
	binding, _ := bindings[0].(map[string]any)
	for field, want := range map[string]any{"name": "main", "type": "postgres", "source": "aws:rds:main", "owner": defaultBindingOwner, "version": float64(1)} {
		if binding[field] != want {
			t.Errorf("ls json binding %s = %v, want %v", field, binding[field], want)
		}
	}

	var rm, stderr bytes.Buffer
	if err := runBindingsRm(context.Background(), newDeps(), root, "main", bindingsOptions{}, &rm, &stderr); err != nil {
		t.Fatalf("runBindingsRm err = %v; stderr=%s", err, stderr.String())
	}
	removed := asJSON(t, rm.String())
	if removed["name"] != "main" || removed["removed"] != true {
		t.Errorf("rm json = %v, want the name it removed and that it was there", removed)
	}
}

func TestBindingCommands(t *testing.T) {
	t.Parallel()

	t.Run("every subcommand addresses a bootstrap", func(t *testing.T) {
		t.Parallel()

		for _, c := range []*cobra.Command{bindingsSetCmd, bindingsRmCmd, bindingsLsCmd, bindingsGenerateCmd} {
			for _, flag := range []string{"preview", "environment"} {
				if c.Flags().Lookup(flag) == nil {
					t.Errorf("`ocel binding %s` cannot address --%s", c.Name(), flag)
				}
			}
		}
	})

	t.Run("only set publishes under a name", func(t *testing.T) {
		t.Parallel()

		owner := bindingsSetCmd.Flags().Lookup("owner")
		if owner == nil {
			t.Fatal("`ocel bindings set` registers no --owner; the publisher is what keeps one from taking another's binding")
		}
		if owner.DefValue != defaultBindingOwner {
			t.Errorf("--owner defaults to %q, want %q", owner.DefValue, defaultBindingOwner)
		}
		for _, c := range []*cobra.Command{bindingsRmCmd, bindingsLsCmd, bindingsGenerateCmd} {
			if c.Flags().Lookup("owner") != nil {
				t.Errorf("`ocel binding %s` registers --owner; only publishing takes a name", c.Name())
			}
		}
	})

	t.Run("`ocel generate` is a different command, and still promises no provider", func(t *testing.T) {
		t.Parallel()

		if bindingsGenerateCmd.Parent() != bindingsCmd {
			t.Errorf("`ocel binding generate` hangs off %v, want the binding command", bindingsGenerateCmd.Parent())
		}
		if generateCmd.Parent() != rootCmd {
			t.Errorf("`ocel generate` hangs off %v, want the root command", generateCmd.Parent())
		}
		if !strings.Contains(generateCmd.Long, "no login, no provider") {
			t.Errorf("`ocel generate` long = %q, want the promise it keeps", generateCmd.Long)
		}
		if !strings.Contains(bindingsGenerateCmd.Long, "provider") {
			t.Errorf("`ocel binding generate` long = %q, want it to say it runs the provider", bindingsGenerateCmd.Long)
		}
	})

	t.Run("`ocel binding` is a different command", func(t *testing.T) {
		t.Parallel()

		if bindingsCmd.Parent() != rootCmd {
			t.Errorf("`ocel binding` hangs off %v, want the root command", bindingsCmd.Parent())
		}
		if unlinkCmd.Parent() != rootCmd {
			t.Errorf("`ocel unlink` hangs off %v, want the root command", unlinkCmd.Parent())
		}
		if bindingsCmd.Parent() != rootCmd {
			t.Errorf("`ocel bindings` hangs off %v, want the root command", bindingsCmd.Parent())
		}
	})
}
