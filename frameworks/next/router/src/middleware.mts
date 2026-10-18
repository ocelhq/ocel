import type { MiddlewareMatcher } from "@framework/next-protocol/route-table";
import { matchesHas } from "./has.mjs";
import type { MiddlewareOutcome } from "./route-result.mjs";

const NULL_BODY_STATUSES = new Set([204, 205, 304]);

export function middlewareResponse(
  outcome: MiddlewareOutcome | undefined,
  status?: number,
): Response {
  if (!outcome) return new Response(null, { status: status ?? 200 });
  const body = NULL_BODY_STATUSES.has(outcome.response.status) ? null : outcome.response.body;
  const response = new Response(body, outcome.response);
  stripMiddlewareHeaders(response.headers);
  return response;
}

export function stripMiddlewareHeaders(headers: Headers): void {
  for (const name of [...headers.keys()]) {
    if (name.startsWith("x-middleware-")) headers.delete(name);
  }
}

export function middlewareMatches(
  matchers: MiddlewareMatcher[] | undefined,
  url: URL,
  headers: Headers,
): boolean {
  if (!matchers || matchers.length === 0) return true;
  return matchers.some(
    (matcher) =>
      new RegExp(matcher.sourceRegex).test(url.pathname) &&
      (matcher.has ?? []).every((has) => matchesHas(has, headers, url)) &&
      !(matcher.missing ?? []).some((has) => matchesHas(has, headers, url)),
  );
}

export function middlewareOverrides(outcome: MiddlewareOutcome | undefined): Set<string> {
  const named = outcome?.response.headers.get("x-middleware-override-headers");
  return new Set(
    named
      ?.split(",")
      .map((name) => name.trim().toLowerCase())
      .filter(Boolean),
  );
}
