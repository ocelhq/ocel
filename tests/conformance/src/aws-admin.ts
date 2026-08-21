import { execFileSync } from "node:child_process";
import { examples, repoRoot } from "./examples";
import { projectSlugForRun } from "./targets/aws";

const prefix = "/ocel/rootstack-preview/";

function aws(args: string[]) {
  return execFileSync("aws", args, {
    encoding: "utf8",
    timeout: 30_000,
    maxBuffer: 16 * 1024 * 1024,
    env: { ...process.env, AWS_RETRY_MODE: "standard", AWS_MAX_ATTEMPTS: "4" },
  }).trim();
}

function destroy(slug: string) {
  const example = examples[0];
  const command = process.env.OCEL_BIN ?? process.execPath;
  const args = process.env.OCEL_BIN
    ? ["destroy", "--preview", "--yes"]
    : [
        `${repoRoot}/packages/ocel/bin/run.js`,
        "destroy",
        "--preview",
        "--yes",
      ];
  execFileSync(command, args, {
    cwd: example.dir,
    stdio: "inherit",
    timeout: 30 * 60_000,
    env: {
      ...process.env,
      OCEL_CONFIG: "ocel.aws.config.ts",
      OCEL_TEST_PROJECT_SLUG: slug,
    },
  });
}

export function destroyProject(slug = projectSlugForRun()) {
  destroy(slug);
}

function projectExists(slug: string) {
  try {
    aws([
      "ssm",
      "get-parameter",
      "--name",
      `${prefix}${slug}`,
      "--output",
      "json",
    ]);
    return true;
  } catch (error) {
    const stderr = String((error as { stderr?: unknown }).stderr ?? "");
    if (stderr.includes("ParameterNotFound")) return false;
    throw error;
  }
}

export function sweepProject(slug = projectSlugForRun()) {
  if (projectExists(slug)) destroyProject(slug);
}

const operation = process.argv[2];
if (operation === "sweep") sweepProject();
else if (operation === "teardown") destroyProject();
else throw new Error(`expected sweep or teardown, got ${operation ?? "nothing"}`);
