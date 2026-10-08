import { readFileSync } from "node:fs";
import path from "node:path";
import { runBuildScript, type ScriptBuild } from "@framework/node-build/script";

export interface SvelteKitBuild extends ScriptBuild {
  outputDir: string;
  buildId: string;
  folder?: string;
}

const HOSTING_FILE = "hosting.json";

const ADAPTER = "@ocel/sveltekit";

const HOSTING_VERSION = 1;

function refuseUnadapted(app: SvelteKitBuild, why: string): Error {
  return new Error(
    `ocel: app "${app.name}" built, but ${why}, so its build did not run through ${ADAPTER}. ` +
      `Add it with \`pnpm add -D ${ADAPTER}\` and name it as the adapter: ` +
      `\`sveltekit({ adapter: ocel() })\` in vite.config on SvelteKit 3, or \`kit: { adapter: ocel() }\` in svelte.config.js on SvelteKit 2, with \`import ocel from "${ADAPTER}"\``,
  );
}

export async function buildSvelteKit(app: SvelteKitBuild): Promise<void> {
  await runBuildScript(app, {
    NODE_ENV: "production",
    OCEL_APP_NAME: app.name,
    OCEL_OUTPUT_DIR: app.outputDir,
    OCEL_APP_FOLDER: app.folder ?? "",
    OCEL_BUILD_ID: app.buildId,
  });
  let hosting: { version?: unknown; framework?: unknown };
  try {
    hosting = JSON.parse(readFileSync(path.join(app.outputDir, HOSTING_FILE), "utf8"));
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") {
      throw refuseUnadapted(app, `nothing wrote ${HOSTING_FILE} to ${app.outputDir}`);
    }
    throw err;
  }
  if (hosting.framework !== "sveltekit") {
    throw refuseUnadapted(
      app,
      `the ${HOSTING_FILE} it wrote names framework ${JSON.stringify(hosting.framework)}`,
    );
  }
  if (hosting.version !== HOSTING_VERSION) {
    throw new Error(
      `ocel: app "${app.name}" built, but ${ADAPTER} wrote ${HOSTING_FILE} version ${JSON.stringify(hosting.version)}, and this CLI reads version ${HOSTING_VERSION}. Install the ${ADAPTER} release that matches this CLI`,
    );
  }
  process.stderr.write(`ocel: SvelteKit app "${app.name}" built\n`);
}
