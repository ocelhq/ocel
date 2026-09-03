const BANNER =
  'import{createRequire as __ocelCreateRequire}from"node:module";var require=__ocelCreateRequire(import.meta.url);';

export function bunArgs(entry, outfile) {
  return [
    entry,
    "--target=node",
    "--format=esm",
    `--banner=${BANNER}`,
    `--outfile=${outfile}`,
  ];
}
