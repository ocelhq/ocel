export function bunArgs(entry, outfile) {
  return [
    entry,
    "--target=node",
    "--format=esm",
    `--outfile=${outfile}`,
  ];
}
