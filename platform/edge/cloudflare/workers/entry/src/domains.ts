export function domainApp(encoded: string | undefined, host: string): string | undefined {
  if (!encoded) return undefined;

  let parsed: unknown;
  try {
    parsed = JSON.parse(encoded);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return undefined;

  const wanted = host.toLowerCase().split(":", 1)[0];
  for (const [domain, app] of Object.entries(parsed as Record<string, unknown>)) {
    if (domain.toLowerCase() !== wanted) continue;
    return typeof app === "string" && app !== "" ? app : undefined;
  }
  return undefined;
}
