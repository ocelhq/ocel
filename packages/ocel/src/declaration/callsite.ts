const frameRE = /\(?((?:\/|file:|[A-Za-z]:\\)[^()]+?):(\d+):\d+\)?$/;

interface Site {
  file: string;
  line: number;
}

function parseFrame(frame: string): Site | undefined {
  const at = frameRE.exec(frame.trim());
  if (!at?.[1] || !at[2]) return undefined;
  return { file: at[1].replace(/^file:\/\//, ""), line: Number(at[2]) };
}

function parentOf(file: string): string {
  return file.slice(0, Math.max(file.lastIndexOf("/"), file.lastIndexOf("\\")));
}

function isUnder(file: string, dir: string): boolean {
  return dir !== "" && (file.startsWith(`${dir}/`) || file.startsWith(`${dir}\\`));
}

/**
 * The file and line of the innermost frame outside this package and outside
 * `node_modules` — the user code whose call reached the SDK. Undefined when the
 * stack names no such frame.
 */
export function callSite(): Site | undefined {
  let ownRoot: string | undefined;
  for (const frame of (new Error().stack ?? "").split("\n").slice(1)) {
    const site = parseFrame(frame);
    if (!site) continue;
    if (ownRoot === undefined) {
      ownRoot = parentOf(parentOf(site.file));
      continue;
    }
    if (isUnder(site.file, ownRoot) || /[/\\]node_modules[/\\]/.test(site.file)) continue;
    return site;
  }
  return undefined;
}

/** The {@link callSite} as `file:line`, or `""`. */
export function declarationSite(): string {
  const site = callSite();
  return site ? `${site.file}:${site.line}` : "";
}

/** The file of the {@link callSite}, or `""`. */
export function callSiteFile(): string {
  return callSite()?.file ?? "";
}
