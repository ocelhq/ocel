package clitest

import (
	"path/filepath"
	"testing"
)

const EnvDeclarationScript = `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

const call = async (method: string, body: unknown) => {
  const res = await fetch(new URL("/app.resources.v1.ResourceService/" + method, process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(method + " failed: " + res.status + " " + (await res.text()));
  return res.json();
};

globalThis.__ocelRegister.push(
  (async () => {
    const definitions = JSON.parse(process.env.OCEL_TEST_ENV_DEFINITIONS!);
    const { cells = [] } = await call("DeclareEnv", { definitions });

    const out = process.env.OCEL_TEST_ENV_CELLS_OUT;
    if (out) await (await import("node:fs/promises")).writeFile(out, JSON.stringify(cells));

    // A file rather than an env var when one is named: a recovery re-runs
    // discovery, and the second pass has to be able to report something
    // different from the first without the parent process re-spawning itself.
    const file = process.env.OCEL_TEST_ENV_PROBLEMS_FILE;
    const problems = JSON.parse(
      file
        ? await (await import("node:fs/promises")).readFile(file, "utf8")
        : process.env.OCEL_TEST_ENV_PROBLEMS!,
    );
    if (problems.length > 0) await call("ReportEnvProblems", { problems });
  })(),
);
export {};
`

const EnvDeclareOnlyScript = `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

globalThis.__ocelRegister.push(
  (async () => {
    const res = await fetch(new URL("/app.resources.v1.ResourceService/DeclareEnv", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ definitions: JSON.parse(process.env.OCEL_TEST_ENV_DEFINITIONS!) }),
    });
    if (!res.ok) throw new Error("DeclareEnv failed: " + res.status + " " + (await res.text()));
  })(),
);
export {};
`

func SetUpEnvGateFixture(t *testing.T, definitions string) string {
	t.Helper()
	return SetUpEnvGateFixtureWith(t, definitions, EnvDeclarationScript)
}

func SetUpEnvGateFixtureWith(t *testing.T, definitions, script string) string {
	t.Helper()
	root, _ := SetUpDeployFixture(t)
	t.Setenv(FakeVarsStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	t.Setenv("OCEL_TEST_ENV_DEFINITIONS", definitions)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS", "[]")
	WriteFile(t, filepath.Join(root, "ocel", "env.ts"), script)
	return root
}
