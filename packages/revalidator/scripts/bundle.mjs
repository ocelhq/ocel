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
