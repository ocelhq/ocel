const BANNER =
  'import{createRequire as __ocelCreateRequire}from"node:module";var require=__ocelCreateRequire(import.meta.url);';

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
