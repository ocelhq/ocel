const sdkModule =
  /[/\\](utils[/\\]callsite|postgres[/\\](pg|index)|blob[/\\](bucket|index))\.[cm]?[jt]s$/;

export function declarationSite(): string {
  for (const frame of (new Error().stack ?? "").split("\n").slice(1)) {
    const at = /\(?((?:\/|file:|[A-Za-z]:\\)[^()]+?):(\d+):\d+\)?$/.exec(frame.trim());
    if (!at?.[1] || !at[2]) continue;
    const file = at[1].replace(/^file:\/\//, "");
    if (sdkModule.test(file) || /[/\\]node_modules[/\\]/.test(file)) continue;
    return `${file}:${at[2]}`;
  }
  return "";
}
