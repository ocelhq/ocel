const BANNER = [
  'import{createRequire as __ocelCreateRequire}from"node:module";',
  'import{fileURLToPath as __ocelFileURLToPath}from"node:url";',
  'import{dirname as __ocelDirname}from"node:path";',
  "var require=__ocelCreateRequire(import.meta.url);",
  "var __ocelFilename=__ocelFileURLToPath(import.meta.url),__ocelDirnameOf=__ocelDirname(__ocelFilename);",
].join("");

export function bunArgs(entry, outfile) {
  return [
    entry,
    "--target=node",
    "--format=esm",
    "--external=sharp",
    "--define=__filename=__ocelFilename",
    "--define=__dirname=__ocelDirnameOf",
    `--banner=${BANNER}`,
    `--outfile=${outfile}`,
  ];
}
