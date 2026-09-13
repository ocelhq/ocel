package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/pkg/constants"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestDeployUsageEdges(t *testing.T) {
	t.Run("an app that uses a shared resource lands a usage edge naming the files it reaches through", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{
			{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "USAGE app=api resource=db--main files=apps/api/src/server.ts") {
			t.Errorf("stdout = %q, want the usage edge to have reached the manifest", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a resource no app uses still provisions and carries no edge", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want the orphan resource to deploy", out)
		}
		if strings.Contains(out, "USAGE ") {
			t.Errorf("stdout = %q, want no usage edge for an orphan resource", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a runtime-computed import in an app fails the deploy closed", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)
		clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "late.ts"), `
const spec = "../../../shared/" + ["d", "b"].join("") + ".js";

export async function late() {
  return await import(spec);
}
`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "apps/api/src/late.ts") {
			t.Errorf("output = %q, want it to name the file holding the unresolvable import", combined)
		}
	})
}

func TestDeployScopesDeliveryToTheUsingApps(t *testing.T) {
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
		{Route: "web", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/web", App: "web"},
	})
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeSharedResourceMonorepo(t, root)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		"DELIVER app=api resources=bucket--uploads,db--main",
		"DELIVER app=web resources=db--main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "DELIVER app=web resources=bucket--uploads") {
		t.Errorf("stdout = %q: web never reaches the bucket, so it receives neither its values nor its live keys", out)
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDeployAttributesAnUnconfiguredProjectToItsOnlyApp(t *testing.T) {
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output", App: "web"},
	})
	root, sockPath := clitest.SetUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DELIVER app=web resources=db--main") {
		t.Errorf("stdout = %q, want the only app of a project that configures none to still reach what it declares", out)
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDeployRefusesWhatItCannotAttribute(t *testing.T) {
	t.Run("a project that builds two apps and names neither", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{
			{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/web", App: "web"},
		})
		root, _ := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{"api", "web", "ocel.config.ts"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})

	t.Run("a built app the config names nothing of", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{
			{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/legacy", App: "legacy"},
		})
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the app no configured app covers to refuse the deploy; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(combined, "legacy") {
			t.Errorf("output = %q, want it to name the app the config covers with nothing", combined)
		}
	})

	t.Run("a configured path that names no directory", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, []manifestbuilder.Function{
			{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
		})
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { name: "aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/ap1", framework: "node" }],
};
`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want a path naming nothing to refuse the deploy rather than ship an app no resource reaches; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{`"api"`, "apps/ap1"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})
}

func TestDeployGrantsAResourceThroughAReference(t *testing.T) {
	functions := []manifestbuilder.Function{
		{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
		{Route: "web", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/web", App: "web"},
	}

	t.Run("an app reaching only the reference is delivered the declared resource", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, functions)
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeReferencingMonorepo(t, root, `reference("RESOURCE_TYPE_POSTGRES", "main")`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"DELIVER app=api resources=bucket--uploads,db--main",
			"DELIVER app=web resources=db--main",
			"USAGE app=web resource=db--main files=apps/web/src/server.ts",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a reference nothing declares refuses the deploy at the reference", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, functions)
		root, _ := clitest.SetUpDeployFixture(t)
		writeReferencingMonorepo(t, root, `reference("RESOURCE_TYPE_POSTGRES", "ghost")`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the dangling reference to refuse the deploy; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{`"ghost"`, "shared/refs.ts:"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %s", combined, want)
			}
		}
	})

	t.Run("an app reaching only a bucket reference is delivered the declared bucket", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, functions)
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeReferencingMonorepo(t, root, `{ ...reference("RESOURCE_TYPE_BUCKET", "uploads"), uploaders: { avatar: {} } }`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"DELIVER app=web resources=bucket--uploads",
			"USAGE app=web resource=bucket--uploads files=apps/web/src/server.ts",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a dangling reference outside the project in a project declaring nothing still names where it was written", func(t *testing.T) {
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, functions)
		root, _ := clitest.SetUpDeployFixture(t)
		writeReferencingMonorepo(t, root, `reference("RESOURCE_TYPE_POSTGRES", "ghost", "/elsewhere/refs.ts:4")`)
		clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), `
export * from "../shared/refs.js";
`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the dangling reference to refuse the deploy; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{`"ghost"`, "/elsewhere/refs.ts:4"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %s", combined, want)
			}
		}
	})
}

func writeReferencingMonorepo(t *testing.T, root, shared string) {
	t.Helper()

	writeSharedResourceMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), `
export * from "../shared/index.js";
export * from "../shared/refs.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "refs.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}

function site(): string {
  const at = /\(?((?:\/|file:)[^()]+?):(\d+):\d+\)?$/.exec(((new Error().stack ?? "").split("\n")[3] ?? "").trim());
  return at ? at[1].replace(/^file:\/\//, "") + ":" + at[2] : "";
}

function reference(type: string, name: string, source = site()) {
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(
    fetch(new URL("/app.resources.v1.ResourceService/Reference", process.env.`+constants.DevServerEnvName+`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ resource: { type, name }, source }),
    }),
  );
  return { name };
}

export const shared = `+shared+`;
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), `
import { shared } from "../../../shared/refs.js";

export function handler() {
  return shared.name;
}
`)
}

func writeSharedResourceMonorepo(t *testing.T, root string) {
	t.Helper()

	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { name: "aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [
    { name: "api", path: "apps/api", framework: "node" },
    { name: "web", path: "apps/web", framework: "node" },
  ],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const uploads = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, uploads } from "../../../shared/index.js";

export function handler() {
  return db.name + uploads.name;
}
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}
