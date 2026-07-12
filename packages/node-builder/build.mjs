import { build } from "esbuild";

// Bundle the builder + its runtime deps (@vercel/nft, typescript) into one
// self-contained .mjs so the CLI can `go:embed` it. The bundle
// runs inside a user project with plain `node` — it must carry ZERO external
// runtime deps. esbuild is only a build-time tool (used above), never imported
// at runtime, so it is not in this bundle.

// The bundled CJS deps (typescript) reference the CommonJS globals `require`,
// `__filename`, and `__dirname`, which don't exist in an ESM output — recreate
// them from import.meta.url so those deps run under ESM.
const cjsShim = [
  "import { createRequire as __nbCreateRequire } from 'node:module';",
  "import { fileURLToPath as __nbFileURLToPath } from 'node:url';",
  "import { dirname as __nbDirname } from 'node:path';",
  "const require = __nbCreateRequire(import.meta.url);",
  "const __filename = __nbFileURLToPath(import.meta.url);",
  "const __dirname = __nbDirname(__filename);",
].join("\n");

await build({
  entryPoints: ["src/cli.ts"],
  outfile: "dist/node-builder.mjs",
  bundle: true,
  platform: "node",
  target: "node20",
  format: "esm",
  // Optional cloud/native deps @vercel/nft's node-pre-gyp tracer references but
  // never needs on the JS-tracing path; leaving them external keeps the bundle
  // lean without breaking it.
  external: ["aws-sdk", "mock-aws-s3", "nock", "node-gyp", "npm"],
  loader: { ".html": "text", ".cs": "text" },
  banner: { js: cjsShim },
});

console.log("built dist/node-builder.mjs");
