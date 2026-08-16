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
)

const fakeLinkPassword = "pw-never-listed-7c31"

func setUpLinkFixture(t *testing.T) string {
	t.Helper()
	root, _ := setUpDeployFixture(t)
	t.Setenv(linkFakeStoreEnvVar, filepath.Join(t.TempDir(), "links.json"))
	return root
}

func postgresLinkJSON(name, host string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "aws:rds:%s",
  "postgres": {"host": %q, "port": 5432, "database": "app", "username": "app", "password": %q}
}`, name, name, host, fakeLinkPassword)
}

func linkSet(t *testing.T, root, body string, opts linkOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runLinkSet(context.Background(), defaultDeps(), root, strings.NewReader(body), opts, &stdout, &stderr); err != nil {
		t.Fatalf("runLinkSet err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func linkLs(t *testing.T, root string, opts linkOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runLinkLs(context.Background(), defaultDeps(), root, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runLinkLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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
	logFormatFlag = logFormatJSON
}

func TestRunLinkSet(t *testing.T) {
	t.Run("the record it publishes is what ls shows, and rm takes it away", func(t *testing.T) {
		root := setUpLinkFixture(t)

		if out := linkSet(t, root, postgresLinkJSON("main", "db.internal"), linkOptions{}); !strings.Contains(out, "main") {
			t.Errorf("set stdout = %q, want it to name the link it published", out)
		}

		listed := linkLs(t, root, linkOptions{})
		for _, want := range []string{"main", "postgres", "aws:rds:main", defaultLinkOwner} {
			if !strings.Contains(listed, want) {
				t.Errorf("ls stdout = %q, want it to show %q", listed, want)
			}
		}

		var rm, stderr bytes.Buffer
		if err := runLinkRm(context.Background(), defaultDeps(), root, "main", linkOptions{}, &rm, &stderr); err != nil {
			t.Fatalf("runLinkRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(rm.String(), "main") {
			t.Errorf("rm stdout = %q, want it to name the link it removed", rm.String())
		}

		if after := linkLs(t, root, linkOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the link gone", after)
		}
	})

	t.Run("refuses to take a name another publisher holds", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("main", "db.internal"), linkOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		err := runLinkSet(context.Background(), defaultDeps(), root,
			strings.NewReader(postgresLinkJSON("main", "other.internal")), linkOptions{owner: "cli"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runLinkSet over another publisher's link err = nil, want a refusal")
		}
		for _, want := range []string{"terraform", "cli"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("refuses to publish as ocel's own provisioning", func(t *testing.T) {
		root := setUpLinkFixture(t)

		var stdout, stderr bytes.Buffer
		err := runLinkSet(context.Background(), defaultDeps(), root,
			strings.NewReader(postgresLinkJSON("main", "db.internal")), linkOptions{owner: "OCEL"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runLinkSet --owner OCEL err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "OCEL") {
			t.Errorf("err = %v, want it to name the publisher it refused", err)
		}
		if listed := linkLs(t, root, linkOptions{}); strings.Contains(listed, "main") {
			t.Errorf("ls = %q, want the refused link never published", listed)
		}
	})

	t.Run("the same publisher bumps the version", func(t *testing.T) {
		root := setUpLinkFixture(t)
		opts := linkOptions{owner: "terraform"}
		linkSet(t, root, postgresLinkJSON("main", "db.internal"), opts)
		if out := linkSet(t, root, postgresLinkJSON("main", "moved.internal"), opts); !strings.Contains(out, "2") {
			t.Errorf("second set stdout = %q, want version 2", out)
		}

		listed := linkLs(t, root, linkOptions{})
		if !strings.Contains(listed, "terraform") {
			t.Errorf("ls stdout = %q, want the publisher that holds the name", listed)
		}
	})

	t.Run("rm takes a link whatever published it", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("main", "db.internal"), linkOptions{owner: "terraform"})

		var stdout, stderr bytes.Buffer
		if err := runLinkRm(context.Background(), defaultDeps(), root, "main", linkOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runLinkRm over another publisher's link err = %v; stderr=%s", err, stderr.String())
		}
		if after := linkLs(t, root, linkOptions{}); strings.Contains(after, "main") {
			t.Errorf("ls after rm = %q, want the link gone", after)
		}
	})

	t.Run("reports nothing to remove", func(t *testing.T) {
		root := setUpLinkFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runLinkRm(context.Background(), defaultDeps(), root, "never-published", linkOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runLinkRm err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "never-published") {
			t.Errorf("rm of a link that was never published = %q, want it to name what it looked for", stdout.String())
		}
	})

	t.Run("refuses stdin that is not a link", func(t *testing.T) {
		root := setUpLinkFixture(t)

		for name, body := range map[string]string{
			"not JSON at all":         "postgres://db.internal/app",
			"a field no link carries": `{"name":"main","postgres":{"host":"db.internal"},"nonsense":true}`,
			"nothing at all on stdin": "",
			"JSON that is not a link": `["main"]`,
			"a link with no name":     `{"postgres":{"host":"db.internal"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runLinkSet(context.Background(), defaultDeps(), root, strings.NewReader(body), linkOptions{}, &stdout, &stderr)
				if err == nil {
					t.Fatalf("runLinkSet(%q) err = nil, want a refusal", body)
				}
				if listed := linkLs(t, root, linkOptions{}); strings.Contains(listed, "main") {
					t.Errorf("ls = %q, want the refused link never published", listed)
				}
			})
		}
	})
}

func TestRunLinkLs(t *testing.T) {
	t.Run("never prints a property value", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("main", "db.internal"), linkOptions{})

		for name, run := range map[string]func() string{
			"human": func() string { return linkLs(t, root, linkOptions{}) },
			"json": func() string {
				jsonOutput(t)
				return linkLs(t, root, linkOptions{})
			},
		} {
			t.Run(name, func(t *testing.T) {
				out := run()
				for _, secret := range []string{fakeLinkPassword, "db.internal", "5432"} {
					if strings.Contains(out, secret) {
						t.Errorf("ls stdout = %q, want no property value printed (found %q)", out, secret)
					}
				}
			})
		}
	})

	t.Run("reports an empty listing", func(t *testing.T) {
		root := setUpLinkFixture(t)
		if out := linkLs(t, root, linkOptions{}); !strings.Contains(out, "ocel link set") {
			t.Errorf("ls with nothing published = %q, want it to name the command that publishes one", out)
		}
	})
}

func customLinkJSON(name string) string {
	return fmt.Sprintf(`{
  "name": %q,
  "source": "terraform:module.network",
  "custom": {"subnetIds": ["subnet-0a1", "subnet-0b2"], "securityGroupIds": ["sg-1"], "port": 5432}
}`, name)
}

func linkGenerate(t *testing.T, root string, opts linkOptions) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runLinkGenerate(context.Background(), defaultDeps(), root, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runLinkGenerate err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	written, err := os.ReadFile(filepath.Join(root, linkTypesFileName))
	if err != nil {
		t.Fatalf("read the generated types: %v", err)
	}
	return stdout.String(), string(written)
}

func renderedPropertyTypes(t *testing.T, written string) []string {
	t.Helper()
	_, declared, ok := strings.Cut(written, "interface Links {\n")
	if !ok {
		t.Fatalf("generated file =\n%s\nnames no Links interface", written)
	}
	body, _, ok := strings.Cut(declared, "\n  }\n")
	if !ok {
		t.Fatalf("generated file =\n%s\nnever closes the Links interface", written)
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

func TestRunLinkGenerate(t *testing.T) {
	t.Run("writes the shape of every published record and none of its values", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("orders", "db.internal"), linkOptions{})
		linkSet(t, root, customLinkJSON("network"), linkOptions{owner: "terraform"})

		out, written := linkGenerate(t, root, linkOptions{})

		for _, want := range []string{
			"// generated by `ocel link generate` from production; do not edit",
			`declare module "@ocel/provider-aws/transform"`,
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
		if !strings.Contains(out, linkTypesFileName) {
			t.Errorf("generate stdout = %q, want it to name the file it wrote", out)
		}
	})

	t.Run("addresses the coordinate its flags name", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("orders", "db.internal"), linkOptions{})

		t.Setenv(fakeInfraClassEnvVar, "preview")
		linkSet(t, root, customLinkJSON("network"), linkOptions{preview: true, environment: "staging", owner: "terraform"})

		_, written := linkGenerate(t, root, linkOptions{preview: true, environment: "staging"})
		if !strings.Contains(written, "from the preview environment staging;") {
			t.Errorf("generated file =\n%s\nwant the header to name the coordinate it read", written)
		}
		if !strings.Contains(written, "network:") || strings.Contains(written, "orders:") {
			t.Errorf("generated file =\n%s\nwant only what that coordinate holds", written)
		}
	})

	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpLinkFixture(t)

		var stdout, stderr bytes.Buffer
		err := runLinkGenerate(context.Background(), defaultDeps(), root, linkOptions{environment: "staging"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runLinkGenerate --environment against production err = nil, want a refusal")
		}
		if _, statErr := os.Stat(filepath.Join(root, linkTypesFileName)); statErr == nil {
			t.Error("a refused generate wrote the file anyway")
		}
	})

	t.Run("names no record when nothing is published", func(t *testing.T) {
		root := setUpLinkFixture(t)

		out, written := linkGenerate(t, root, linkOptions{})
		if !strings.Contains(written, "interface Links {\n  }\n") {
			t.Errorf("generated file =\n%s\nwant an interface naming no record", written)
		}
		if !strings.Contains(written, "interface LinksGenerated {\n    generated: true;\n  }") {
			t.Errorf("generated file =\n%s\nwant it to mark itself generated, so an empty coordinate still closes every name", written)
		}
		if !strings.Contains(out, "Nothing is published") || !strings.Contains(out, "no link name open") {
			t.Errorf("generate stdout = %q, want it to say nothing was published and that no name stays open", out)
		}
	})
}

func TestLinkSubstrate(t *testing.T) {
	t.Run("refuses --environment without --preview", func(t *testing.T) {
		root := setUpLinkFixture(t)

		var stdout, stderr bytes.Buffer
		for name, err := range map[string]error{
			"set": runLinkSet(context.Background(), defaultDeps(), root, strings.NewReader(postgresLinkJSON("main", "db.internal")), linkOptions{environment: "staging"}, &stdout, &stderr),
			"rm":  runLinkRm(context.Background(), defaultDeps(), root, "main", linkOptions{environment: "staging"}, &stdout, &stderr),
			"ls":  runLinkLs(context.Background(), defaultDeps(), root, linkOptions{environment: "staging"}, &stdout, &stderr),
		} {
			if err == nil {
				t.Errorf("`ocel link %s --environment` against production err = nil, want a refusal", name)
				continue
			}
			if !strings.Contains(err.Error(), "--preview") {
				t.Errorf("`ocel link %s` err = %v, want it to name the flag that selects the substrate overrides live on", name, err)
			}
		}
	})

	t.Run("a preview link is not a production link", func(t *testing.T) {
		root := setUpLinkFixture(t)
		linkSet(t, root, postgresLinkJSON("main", "prod.internal"), linkOptions{})

		t.Setenv(fakeInfraClassEnvVar, "preview")
		if out := linkLs(t, root, linkOptions{preview: true}); strings.Contains(out, "main") {
			t.Errorf("preview ls = %q, want no production link listed", out)
		}

		linkSet(t, root, postgresLinkJSON("staged", "staging.internal"), linkOptions{preview: true, environment: "staging"})
		if out := linkLs(t, root, linkOptions{preview: true, environment: "staging"}); !strings.Contains(out, "staged") {
			t.Errorf("ls --preview --environment staging = %q, want the link that environment holds", out)
		}

		t.Setenv(fakeInfraClassEnvVar, "production")
		if out := linkLs(t, root, linkOptions{}); strings.Contains(out, "staged") {
			t.Errorf("production ls = %q, want no preview link listed", out)
		}
	})
}

func TestRunLinkJSONOutput(t *testing.T) {
	root := setUpLinkFixture(t)
	jsonOutput(t)

	set := asJSON(t, linkSet(t, root, postgresLinkJSON("main", "db.internal"), linkOptions{}))
	if set["name"] != "main" || set["version"] != float64(1) {
		t.Errorf("set json = %v, want the name and version it published", set)
	}

	listed := asJSON(t, linkLs(t, root, linkOptions{}))
	links, ok := listed["links"].([]any)
	if !ok || len(links) != 1 {
		t.Fatalf("ls json = %v, want one link", listed)
	}
	link, _ := links[0].(map[string]any)
	for field, want := range map[string]any{"name": "main", "type": "postgres", "source": "aws:rds:main", "owner": defaultLinkOwner, "version": float64(1)} {
		if link[field] != want {
			t.Errorf("ls json link %s = %v, want %v", field, link[field], want)
		}
	}

	var rm, stderr bytes.Buffer
	if err := runLinkRm(context.Background(), defaultDeps(), root, "main", linkOptions{}, &rm, &stderr); err != nil {
		t.Fatalf("runLinkRm err = %v; stderr=%s", err, stderr.String())
	}
	removed := asJSON(t, rm.String())
	if removed["name"] != "main" || removed["removed"] != true {
		t.Errorf("rm json = %v, want the name it removed and that it was there", removed)
	}
}

func TestLinkCommands(t *testing.T) {
	t.Parallel()

	t.Run("every subcommand addresses a substrate", func(t *testing.T) {
		t.Parallel()

		for _, c := range []*cobra.Command{linkSetCmd, linkRmCmd, linkLsCmd, linkGenerateCmd} {
			for _, flag := range []string{"preview", "environment"} {
				if c.Flags().Lookup(flag) == nil {
					t.Errorf("`ocel link %s` cannot address --%s", c.Name(), flag)
				}
			}
		}
	})

	t.Run("only set publishes under a name", func(t *testing.T) {
		t.Parallel()

		owner := linkSetCmd.Flags().Lookup("owner")
		if owner == nil {
			t.Fatal("`ocel link set` registers no --owner; the publisher is what keeps one from taking another's link")
		}
		if owner.DefValue != defaultLinkOwner {
			t.Errorf("--owner defaults to %q, want %q", owner.DefValue, defaultLinkOwner)
		}
		for _, c := range []*cobra.Command{linkRmCmd, linkLsCmd, linkGenerateCmd} {
			if c.Flags().Lookup("owner") != nil {
				t.Errorf("`ocel link %s` registers --owner; only publishing takes a name", c.Name())
			}
		}
	})

	t.Run("`ocel generate` is a different command, and still promises no provider", func(t *testing.T) {
		t.Parallel()

		if linkGenerateCmd.Parent() != linkCmd {
			t.Errorf("`ocel link generate` hangs off %v, want the link command", linkGenerateCmd.Parent())
		}
		if generateCmd.Parent() != rootCmd {
			t.Errorf("`ocel generate` hangs off %v, want the root command", generateCmd.Parent())
		}
		if !strings.Contains(generateCmd.Long, "no login, no provider") {
			t.Errorf("`ocel generate` long = %q, want the promise it keeps", generateCmd.Long)
		}
		if !strings.Contains(linkGenerateCmd.Long, "provider") {
			t.Errorf("`ocel link generate` long = %q, want it to say it runs the provider", linkGenerateCmd.Long)
		}
	})

	t.Run("`ocel console link` is a different command", func(t *testing.T) {
		t.Parallel()

		if consoleLinkCmd.Parent() != consoleCmd {
			t.Errorf("`ocel console link` hangs off %v, want the console command", consoleLinkCmd.Parent())
		}
		if linkCmd.Parent() != rootCmd {
			t.Errorf("`ocel link` hangs off %v, want the root command", linkCmd.Parent())
		}
	})
}
