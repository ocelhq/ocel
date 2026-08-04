// The one esbuild invocation the deployable artifact is built from. It lives
// here rather than inline in build-zip.mjs so the test that imports the bundle
// builds the same bytes the zip does: a flag that only the zip script carries is
// a flag nothing checks.

// What lets an ESM bundle hold CJS dependencies. esbuild rewrites every require()
// inside a bundled CJS module to its own __require shim, and that shim throws
// unless a real `require` is in scope — which, in a .mjs, there is not. The AWS
// SDK reaches for node: builtins that way at module scope, so without this the
// function throws while the module graph is still loading, on every invocation,
// before the handler exists.
const BANNER =
  'import{createRequire as __ocelCreateRequire}from"node:module";var require=__ocelCreateRequire(import.meta.url);';

// Nothing here is native, so the whole function — the AWS SDK clients and the
// shared cache primitives included — folds into one file and the zip carries no
// node_modules at all.
export function esbuildArgs(entry, outfile) {
  return [
    entry,
    "--bundle",
    "--platform=node",
    "--target=node22",
    "--format=esm",
    `--banner:js=${BANNER}`,
    `--outfile=${outfile}`,
  ];
}
