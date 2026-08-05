// The one esbuild invocation the deployable artifact is built from. It lives
// here rather than inline in build-zip.mjs so the test that imports the bundle
// builds the same bytes the zip does: a flag that only the zip script carries is
// a flag nothing checks.
//
// The consumer's only dependency is aws4fetch, which is ESM and reaches for no
// node builtins, so the whole function folds into one file with no require shim
// and no node_modules in the archive.
export function esbuildArgs(entry, outfile) {
  return [
    entry,
    "--bundle",
    "--platform=node",
    "--target=node22",
    "--format=esm",
    `--outfile=${outfile}`,
  ];
}
