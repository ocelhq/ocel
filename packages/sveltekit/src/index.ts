import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Adapter, Builder } from "@sveltejs/kit";
import {
  describeHosting,
  describeStaticRules,
  FRAMEWORK,
  ROOT_FUNCTION,
  tableServedFiles,
} from "./output.js";
import { bundleFunction } from "./trace.js";

/** How the adapter writes the build. */
export interface Options {
  /** Where `vite build` writes the deployable output when `ocel build` does not name a directory. */
  out?: string;
}

const OUTPUT_DIR_ENV = "OCEL_OUTPUT_DIR";
const APP_NAME_ENV = "OCEL_APP_NAME";

const FUNCTION_DIR = "functions/index.func";

const files = fileURLToPath(new URL("../files", import.meta.url));

interface KitConfig {
  paths: { base: string };
  version: { name: string };
}

type AnyBuilder = Omit<Builder, "generateServerInstance" | "config"> & {
  config: Builder["config"] & { kit?: KitConfig };
  mimeTypes?: Record<string, string>;
  generateServerInstance?: Builder["generateServerInstance"];
  generateManifest?: (opts: { relativePath: string }) => string;
  writeServer: (dest: string) => string[];
};

function readKitConfig(builder: AnyBuilder): KitConfig {
  const config = builder.config as unknown as Partial<KitConfig> & { kit: KitConfig };
  return config.paths ? (config as KitConfig) : config.kit;
}

function writeServer(builder: AnyBuilder, tmp: string): void {
  if (builder.generateServerInstance) {
    builder.generateServerInstance(`${tmp}/server.js`);
    return;
  }
  if (!builder.generateManifest) {
    throw new Error(
      "@ocel/sveltekit: this SvelteKit can neither generate a server instance nor a manifest; use @sveltejs/kit 2.31 or later",
    );
  }
  builder.writeServer(`${tmp}/server`);
  writeFileSync(
    `${tmp}/server.js`,
    [
      `import { Server } from "./server/index.js";`,
      `export const server = new Server(${builder.generateManifest({ relativePath: "./server" })});`,
      "",
    ].join("\n"),
  );
}

/**
 * The SvelteKit adapter that builds the app into what `ocel deploy` ships: one function that
 * renders every route, the client and prerendered files beside it, and the hosting that states
 * which of them are content-hashed. `node build` serves the same output in a container.
 */
export default function ocel(options: Options = {}): Adapter {
  return {
    name: "@ocel/sveltekit",
    supports: {
      read: () => true,
      instrumentation: () => true,
    },
    async adapt(kitBuilder) {
      const builder = kitBuilder as AnyBuilder;
      const out = resolve(process.env[OUTPUT_DIR_ENV] || options.out || "build");
      const kit = readKitConfig(builder);
      const base = kit.paths.base;
      const appPath = builder.getAppPath();
      const tmp = builder.getBuildDirectory("ocel");
      const staticDir = join(out, "static");

      for (const owned of ["static", "functions", "hosting.json", "index.js"]) {
        rmSync(join(out, owned), { force: true, recursive: true });
      }
      rmSync(tmp, { force: true, recursive: true });
      mkdirSync(tmp, { recursive: true });

      builder.log.minor("Copying assets");
      const clientFiles = builder.writeClient(`${staticDir}${base}`);
      const prerenderedFiles = builder.writePrerendered(`${staticDir}${base}`);

      builder.log.minor("Generating the function");
      const entry = `${tmp}/entry.js`;
      builder.copy(`${files}/entry.js`, entry);
      writeServer(builder, tmp);
      const table = tableServedFiles({
        root: staticDir,
        base,
        appPath,
        clientFiles,
        prerenderedFiles,
        prerenderedPaths: builder.prerendered.paths,
        redirects: builder.prerendered.redirects,
        mimeTypes: builder.mimeTypes,
      });
      writeFileSync(`${tmp}/served.js`, `export const served = ${JSON.stringify(table)};\n`);
      if (builder.generateServerInstance && builder.hasServerInstrumentationFile?.()) {
        builder.instrument({
          entrypoint: entry,
          instrumentation: `${builder.getServerDirectory()}/instrumentation.server.js`,
          initializer: builder.createInstrumentationInitializer({ outputDirectory: tmp }),
        });
      }

      const functionDir = join(out, FUNCTION_DIR);
      const bundled = await bundleFunction(entry, functionDir, builder.log);
      const entryDir = join(functionDir, bundled.entryFile, "..");
      builder.copy(staticDir, join(entryDir, "static"));
      writeFileSync(
        join(functionDir, "function-config.json"),
        JSON.stringify({
          framework: { name: FRAMEWORK },
          entryFile: bundled.entryFile,
          id: ROOT_FUNCTION,
          app: process.env[APP_NAME_ENV] ?? "",
        }),
      );

      builder.copy(`${files}/serve.js`, join(out, "index.js"), {
        replace: { ENTRY: `./${FUNCTION_DIR}/${bundled.entryFile}` },
      });
      writeFileSync(
        join(out, "hosting.json"),
        JSON.stringify(describeHosting(kit.version.name, describeStaticRules(appPath))),
      );
    },
  };
}
