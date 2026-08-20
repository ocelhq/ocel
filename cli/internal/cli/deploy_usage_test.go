package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func TestDeployUsageEdges(t *testing.T) {
	t.Run("an app that uses a shared resource lands a usage edge naming the files it reaches through", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})
		root, sockPath := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "USAGE app=api resource=db--main files=apps/api/src/server.ts") {
			t.Errorf("stdout = %q, want the usage edge to have reached the manifest", out)
		}

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a resource no app uses still provisions and carries no edge", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		root, sockPath := setUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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

		waitForNoStaleSocket(t, sockPath)
	})

	t.Run("a runtime-computed import in an app fails the deploy closed", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		root, _ := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)
		writeFile(t, filepath.Join(root, "apps", "api", "src", "late.ts"), `
const spec = "../../../shared/" + ["d", "b"].join("") + ".js";

export async function late() {
  return await import(spec);
}
`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	d := defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		{Name: "web", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/web", Framework: "express", App: "web"},
	})
	root, sockPath := setUpDeployFixture(t)
	writeSharedResourceMonorepo(t, root)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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

	waitForNoStaleSocket(t, sockPath)
}

func TestDeployAttributesAnUnconfiguredProjectToItsOnlyApp(t *testing.T) {
	d := defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, []manifestbuilder.Function{
		{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output", Framework: "express", App: "web"},
	})
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DELIVER app=web resources=db--main") {
		t.Errorf("stdout = %q, want the only app of a project that configures none to still reach what it declares", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestDeployRefusesWhatItCannotAttribute(t *testing.T) {
	t.Run("a project that builds two apps and names neither", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
			{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/web", Framework: "express", App: "web"},
		})
		root, _ := setUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
			{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/legacy", Framework: "express", App: "legacy"},
		})
		root, _ := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the app no configured app covers to refuse the deploy; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(combined, "legacy") {
			t.Errorf("output = %q, want it to name the app the config covers with nothing", combined)
		}
	})

	t.Run("a configured path that names no directory", func(t *testing.T) {
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})
		root, _ := setUpDeployFixture(t)
		writeUsageMonorepo(t, root)
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/ap1", framework: "express" }],
};
`)

		var stdout, stderr bytes.Buffer
		err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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

func writeSharedResourceMonorepo(t *testing.T, root string) {
	t.Helper()

	writeUsageMonorepo(t, root)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [
    { name: "api", path: "apps/api", framework: "express" },
    { name: "web", path: "apps/web", framework: "express" },
  ],
};
`)
	writeFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const uploads = declareBucket("uploads", new Error().stack ?? "");
`)
	writeFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	writeFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, uploads } from "../../../shared/index.js";

export function handler() {
  return db.name + uploads.name;
}
`)
	writeFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}

func writeUsageMonorepo(t *testing.T, root string) {
	t.Helper()

	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
export * from "../shared/index.js";
`)
	writeFile(t, filepath.Join(root, "shared", "declare.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}

function register(body: Record<string, unknown>) {
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(
    fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  );
}

export function declarePostgres(name: string, stack: string) {
  register({ resource: { type: "LINK_TYPE_POSTGRES", name }, postgres: { version: "17" }, stack });
  return { name };
}

export function declareBucket(name: string, stack: string) {
  register({ resource: { type: "LINK_TYPE_BUCKET", name }, bucket: {}, stack });
  return { name };
}
`)
	writeFile(t, filepath.Join(root, "shared", "db.ts"), `
import { declarePostgres } from "./declare.js";

export const db = declarePostgres("main", new Error().stack ?? "");
`)
	writeFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
`)
	writeFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}
