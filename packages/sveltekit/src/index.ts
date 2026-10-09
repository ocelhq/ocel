import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { traceIntoFunction } from "@framework/node-build/function-trace";
import { BuildOutput, ROOT_FUNCTION, toSlash } from "@framework/node-build/output";
import type { Adapter, Builder } from "@sveltejs/kit";
import { describeHosting, describeStaticRules, FRAMEWORK, tableServedFiles } from "./output.js";

/** How the adapter writes the build. */
export interface Options {
  /** Where `vite build` writes the deployable output when `ocel build` does not name a directory. */
  out?: string;
}

const SERVE_FILE = "index.js";

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
      const output = BuildOutput.fromEnv({ dir: options.out || "build", app: "" });
      const kit = readKitConfig(builder);
      const base = kit.paths.base;
      const appPath = builder.getAppPath();
      const tmp = builder.getBuildDirectory("ocel");
      const staticDir = output.staticDir;

      output.clean();
      rmSync(join(output.dir, SERVE_FILE), { force: true });
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

      const functionDir = output.functionDir(ROOT_FUNCTION);
      const entryFile = await traceIntoFunction(output, ROOT_FUNCTION, entry, builder.log);
      builder.copy(staticDir, join(functionDir, entryFile, "..", "static"));
      output.writeFunctionConfig(ROOT_FUNCTION, FRAMEWORK, entryFile);

      builder.copy(`${files}/serve.js`, join(output.dir, SERVE_FILE), {
        replace: { ENTRY: `./${toSlash(relative(output.dir, functionDir))}/${entryFile}` },
      });
      output.writeHosting(describeHosting(kit.version.name, describeStaticRules(appPath)));
    },
  };
}
