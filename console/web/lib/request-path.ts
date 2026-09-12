export const REQUEST_PATH_HEADER = "x-ocel-path";

export function safeRedirect(target: string | null | undefined): string {
  if (!target?.startsWith("/") || target.startsWith("//") || target.startsWith("/\\")) {
    return "/";
  }
  return target;
}
