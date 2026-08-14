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

export function declarePostgres(name: string) {
  const stack = new Error().stack ?? "";
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(
    fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        resource: { type: "ocel:postgres", name },
        postgres: { version: "17" },
        stack,
      }),
    }),
  );
  return { name };
}
`)
	writeFile(t, filepath.Join(root, "shared", "db.ts"), `
import { declarePostgres } from "./declare.js";

export const db = declarePostgres("main");
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
