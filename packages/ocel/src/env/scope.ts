// EnvScopeError is a variable read by an app the variable was never scoped to.
// It names the scope and the binding because those are the two facts that
// explain it, and it is thrown rather than yielding undefined so a folder
// binding is never something a developer has to infer from a missing value.
export class EnvScopeError extends Error {
  override name = "EnvScopeError";
}

// APP_FOLDER_ENV_VAR carries the folder the running app binds. It is the only
// thing the runtime knows about folders: values arrive already resolved under
// their bare key names, so this exists solely to explain an out-of-scope read.
export const APP_FOLDER_ENV_VAR = "OCEL_APP_FOLDER";

// keyDelimiter is what the variable store separates a value key's components
// with, and is therefore forbidden in every user-chosen name.
const KEY_DELIMITER = "#";

// scopeProblem describes a scope the store cannot address or a reader could
// mistake for another. The project root is the absence of a scope, so naming
// it — as "/" or as an empty list — is reported rather than silently accepted
// as a second spelling of unscoped.
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

// assertInScope refuses a read by an app the key was never scoped to. Nesting
// plays no part: a binding is matched whole, exactly as resolution matched it.
export function assertInScope(key: string, folders: readonly string[]): void {
  if (folders.length === 0) return;

  const binding = process.env[APP_FOLDER_ENV_VAR] ?? "";
  if (folders.includes(binding)) return;

  throw new EnvScopeError(
    `'${key}' is scoped to ${folders.join(", ")}, but this app is bound to ${
      binding === "" ? "the project root" : binding
    }. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope.`,
  );
}

// callSite is the file a declaration was written in, so a complaint that spans
// the declaration and the config that binds its folders can name both. It is
// best-effort: a bundle built without source maps reports its own path.
export function callSite(): string {
  for (const line of (new Error().stack ?? "").split("\n").slice(1)) {
    const file = /\(?((?:\/|file:|[A-Za-z]:\\)[^()]+?):\d+:\d+\)?$/.exec(
      line.trim(),
    )?.[1];
    if (file && !/[/\\]env[/\\](index|scope|declare|definition)\./.test(file)) {
      return file.replace(/^file:\/\//, "");
    }
  }
  return "";
}
