export function callSite(): string {
  for (const line of (new Error().stack ?? "").split("\n").slice(1)) {
    const file = /\(?((?:\/|file:|[A-Za-z]:\\)[^()]+?):\d+:\d+\)?$/.exec(
      line.trim(),
    )?.[1];
    if (
      file &&
      !/[/\\]env[/\\](index|edge|scope|declare|definition|errors|schema|callsite)\.[cm]?[jt]s$/.test(file)
    ) {
      return file.replace(/^file:\/\//, "");
    }
  }
  return "";
}
