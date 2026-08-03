// The one esbuild invocation the deployable artifact is built from. It lives
// here rather than inline in build-zip.mjs so the test that imports the bundle
// builds the same bytes the zip does: a flag that only the zip script carries is
// a flag nothing checks.

// What lets an ESM bundle hold CJS dependencies. esbuild rewrites every require()
// inside a bundled CJS module to its own __require shim, and that shim throws
// unless a real `require` is in scope — which, in a .mjs, there is not. The AWS
// SDK and undici reach for node: builtins that way at module scope, so without
// this the function throws
//
//   Dynamic require of "node:https" is not supported
//
// while the module graph is still loading, on every invocation, before the
// handler exists. Defining `require` at the top of the bundle is what the shim
// looks for, and createRequire resolves against the bundle's own location, which
// is all the builtins need.
const BANNER =
  'import{createRequire as __ocelCreateRequire}from"node:module";var require=__ocelCreateRequire(import.meta.url);';

// sharp is external: a native addon cannot be bundled, and it is the only
// dependency that has to arrive as a real node_modules tree. Everything else —
// the AWS SDK, undici, ipaddr.js — is pure JS and folds into the one file.
export function esbuildArgs(entry, outfile) {
  return [
    entry,
    "--bundle",
    "--platform=node",
    "--target=node22",
    "--format=esm",
    "--external:sharp",
    `--banner:js=${BANNER}`,
    `--outfile=${outfile}`,
  ];
}
