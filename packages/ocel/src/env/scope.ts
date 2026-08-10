export class EnvScopeError extends Error {
  override name = "EnvScopeError";
}

export const APP_FOLDER_ENV_VAR = "OCEL_APP_FOLDER";

const KEY_DELIMITER = "#";

export function scopeProblem(folders: readonly string[]): string | undefined {
  if (folders.length === 0) {
    return "an empty folder scope says nothing. Leave 'folders' off to keep the variable at the project root.";
  }
  const seen = new Set<string>();
  for (const folder of folders) {
    if (seen.has(folder)) {
      return `folder '${folder}' is named twice. A scoped variable holds one value per folder it names.`;
    }
    seen.add(folder);

    const problem = folderProblem(folder);
    if (problem) return `folder '${folder}': ${problem}`;
  }
  return undefined;
}

function folderProblem(folder: string): string | undefined {
  if (!folder.startsWith("/")) return "a folder path must start with '/'.";
  if (folder === "/")
    return "'/' is the project root, which is what an unscoped variable already uses. Leave 'folders' off instead.";
  if (folder.endsWith("/")) return "a folder path must not end with '/'.";
  if (folder.includes("//")) return "a folder path has no empty segments.";
  if (folder.includes(KEY_DELIMITER))
    return `a folder path may not contain '${KEY_DELIMITER}'.`;
  return undefined;
}

export function inScope(folders: readonly string[]): boolean {
  if (folders.length === 0) return true;
  return folders.includes(process.env[APP_FOLDER_ENV_VAR] ?? "");
}

export function assertInScope(key: string, folders: readonly string[]): void {
  if (inScope(folders)) return;

  const binding = process.env[APP_FOLDER_ENV_VAR] ?? "";
  throw new EnvScopeError(
    `'${key}' is scoped to ${folders.join(", ")}, but this app is bound to ${
      binding === "" ? "the project root" : binding
    }. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope.`,
  );
}

export function callSite(): string {
  for (const line of (new Error().stack ?? "").split("\n").slice(1)) {
    const file = /\(?((?:\/|file:|[A-Za-z]:\\)[^()]+?):\d+:\d+\)?$/.exec(
      line.trim(),
    )?.[1];
    if (
      file &&
      !/[/\\]env[/\\](index|edge|scope|declare|definition|errors)\./.test(file)
    ) {
      return file.replace(/^file:\/\//, "");
    }
  }
  return "";
}
