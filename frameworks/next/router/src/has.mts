import type { RouteHas } from "@framework/next-protocol/route-table";

export function isOnDemandRevalidate(request: Request, config: { bypassToken?: string }): boolean {
  return (
    config.bypassToken !== undefined &&
    config.bypassToken !== "" &&
    request.headers.get("x-prerender-revalidate") === config.bypassToken
  );
}

export function shouldBypass(
  request: Request,
  url: URL,
  config: { bypassFor?: RouteHas[]; bypassToken?: string },
): boolean {
  if (isOnDemandRevalidate(request, config)) return true;
  return (config.bypassFor ?? []).some((has) => matchesHas(has, request.headers, url));
}

export function matchesHas(has: RouteHas, headers: Headers, url: URL): boolean {
  const value = hasValue(has, headers, url);
  if (!value) return false;
  if (!has.value) return true;

  const candidate = (Array.isArray(value) ? value[value.length - 1] : value) ?? "";
  try {
    return new RegExp(`^${has.value}$`).test(candidate);
  } catch {
    return false;
  }
}

function hasValue(has: RouteHas, headers: Headers, url: URL): string | string[] | undefined {
  switch (has.type) {
    case "header":
      return headers.get(has.key) ?? undefined;
    case "cookie":
      return cookieValue(headers.get("cookie"), has.key);
    case "query": {
      const values = url.searchParams.getAll(has.key);
      if (values.length === 0) return undefined;
      return values.length === 1 ? values[0] : values;
    }
    case "host":
      return (url.host.split(":", 1)[0] ?? "").toLowerCase();
  }
}

function cookieValue(header: string | null, key: string): string | undefined {
  for (const part of header?.split(";") ?? []) {
    const eq = part.indexOf("=");
    if (eq > 0 && part.slice(0, eq).trim() === key) {
      return part.slice(eq + 1).trim();
    }
  }
  return undefined;
}
