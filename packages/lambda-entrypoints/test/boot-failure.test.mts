import { execFile } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { afterAll, beforeAll, expect, test } from "vitest";

const execFileAsync = promisify(execFile);
const membraneSrc = resolve(dirname(fileURLToPath(import.meta.url)), "../src/shared/membrane.mts");

let dir: string;

beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "ocel-boot-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

async function runChild(
  body: string,
  env: Record<string, string> = {},
): Promise<{ code: number; stderr: string }> {
  const file = join(dir, `child-${Math.random().toString(36).slice(2)}.mts`);
  await writeFile(file, `import { reportFatalBoot } from ${JSON.stringify(membraneSrc)};\n${body}\n`);
  try {
    const { stderr } = await execFileAsync(process.execPath, [file], { env: { ...process.env, ...env } });
    return { code: 0, stderr };
  } catch (err: any) {
    return { code: err.code ?? -1, stderr: err.stderr ?? "" };
  }
}

test("a fatal boot error survives an immediate process.exit", async () => {
  const { code, stderr } = await runChild(
    `reportFatalBoot(new Error("SyntaxError: boom-from-boot"));\nprocess.exit(1);`,
  );

  expect(stderr).toContain("boom-from-boot");
  expect(code).toBe(1);
});

test("reporting a fatal boot error does not depend on the control socket", async () => {
  const { stderr } = await runChild(
    `reportFatalBoot(new Error("no-socket-needed"));\nprocess.exit(1);`,
    { OCEL_CONTROL_SOCKET: "" },
  );

  expect(stderr).toContain("no-socket-needed");
});

test("a fatal boot error reports the stack when there is one", async () => {
  const { stderr } = await runChild(
    `const err = new Error("with-stack");\nreportFatalBoot(err);\nprocess.exit(1);`,
  );

  expect(stderr).toContain("with-stack");
  expect(stderr).toMatch(/at\s/);
});
