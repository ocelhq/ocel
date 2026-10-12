import { refuseOverlappingRepeats } from "./ocel-route-pattern.mjs";

export type TrailingSlash = "never" | "always" | "ignore";

export interface CacheRule {
  revalidate: number;
  expire?: number;
  allowQuery?: string[];
  tags?: string[];
  bypassToken?: string;
}

export interface Route {
  src: string;
  function: string;
  cache?: CacheRule;
}

export interface HeaderRule {
  src: string;
  headers: Record<string, string>;
}

export interface RedirectRule {
  src: string;
  location: string;
  status: 301 | 302 | 307 | 308;
}

export interface OcelRouteTable {
  version: 1;
  basePath: string;
  trailingSlash: TrailingSlash;
  headers: HeaderRule[];
  redirects: RedirectRule[];
  routes: Route[];
  fallback: { function: string };
}

export const ocelRouteTableVar = "OCEL_ROUTE_TABLE";

const maxRepeat = 1000;
const minBypassToken = 16;
const longestMatchedPath = 4096;

const headerName = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;
const bypassTokenCharacters = /^[A-Za-z0-9_-]+$/;
const groupReference = /\$(\d+)/g;
const repeatBound = /^\{(\d+)(?:,(\d*))?\}/;
const redirectStatuses = [301, 302, 307, 308];
const absoluteLocation = /^https?:\/\//i;
const sameSiteLocation = /^\/(?![/\\])/;
const unsendableHeaderValue = /[^\t\x20-\x7e\x80-\xff]/;
const leadingSlashes = /^[/\\\s]+/;

type Refusal = string | undefined;

function isAlphanumeric(c: string): boolean {
  return /^[0-9A-Za-z]$/.test(c);
}

function refuseEscape(c: string, inClass: boolean): Refusal {
  if ("dDwWnrt".includes(c)) return undefined;
  if ("bB".includes(c) && !inClass) return undefined;
  if (c.charCodeAt(0) < 0x80 && !isAlphanumeric(c)) return undefined;
  return `a pattern escapes with \\d, \\D, \\w, \\W, \\n, \\r, \\t, \\b, \\B or a punctuation mark, and \\${c} is none`;
}

function repeatsPast(bounds: (string | undefined)[]): boolean {
  return bounds.some((bound) => bound !== undefined && bound !== "" && Number(bound) > maxRepeat);
}

function refuseUnsharedSyntax(src: string): Refusal {
  if (src === "") return "a pattern is not empty";
  let inClass = false;
  for (let i = 0; i < src.length; i++) {
    const c = src[i]!;
    if (c === "\\") {
      i++;
      if (i === src.length) return "a pattern does not end in a backslash";
      const refused = refuseEscape(src[i]!, inClass);
      if (refused) return refused;
    } else if (inClass) {
      if (c === "[" && src.startsWith("[:", i)) return "a pattern names no POSIX class";
      inClass = c !== "]";
    } else if (c === "[") {
      inClass = true;
      if (
        src
          .slice(i + 1)
          .replace(/^\^/, "")
          .startsWith("]")
      ) {
        return "a pattern names no empty or ]-first class";
      }
    } else if (c === "(" && src.startsWith("(?", i) && !src.startsWith("(?:", i)) {
      return "a pattern groups with ( or (?:, and uses no lookaround, named group or flag";
    } else if (c === "{") {
      const bounds = repeatBound.exec(src.slice(i));
      if (bounds && repeatsPast([bounds[1], bounds[2]])) {
        return `a pattern repeats at most ${maxRepeat} times`;
      }
    }
  }
  return undefined;
}

function repeatsAgain(rest: string): boolean {
  if (rest.startsWith("*") || rest.startsWith("+")) return true;
  const bounds = repeatBound.exec(rest);
  if (!bounds) return false;
  const most = bounds[0].includes(",") ? bounds[2] : bounds[1];
  return most === undefined || most === "" || Number(most) > 1;
}

function refuseAmbiguousRepeat(src: string): Refusal {
  const groups: boolean[] = [];
  let inClass = false;
  for (let i = 0; i < src.length; i++) {
    const c = src[i]!;
    if (c === "\\") i++;
    else if (inClass) inClass = c !== "]";
    else if (c === "[") inClass = true;
    else if (c === "(") {
      groups.push(false);
      if (src.startsWith("(?:", i)) i += 2;
    } else if (c === ")" && groups.length > 0) {
      const ambiguous = groups.pop();
      if (ambiguous && repeatsAgain(src.slice(i + 1))) {
        return "a pattern repeats no group that holds a quantifier or an alternation";
      }
    } else if ("|*+?".includes(c) || (c === "{" && repeatBound.test(src.slice(i)))) {
      groups.fill(true);
    }
  }
  return undefined;
}

function countGroups(src: string): number {
  let groups = 0;
  let inClass = false;
  for (let i = 0; i < src.length; i++) {
    const c = src[i]!;
    if (c === "\\") i++;
    else if (inClass) inClass = c !== "]";
    else if (c === "[") inClass = true;
    else if (c === "(" && !src.startsWith("(?", i)) groups++;
  }
  return groups;
}

function anchorPattern(src: string): string {
  return `^(?:${src})$`;
}

interface Pattern {
  expression: RegExp;
  groups: number;
}

function compilePattern(src: string): Pattern | string {
  const refused = refuseUnsharedSyntax(src) ?? refuseAmbiguousRepeat(src);
  if (refused) return refused;
  let expression: RegExp;
  try {
    expression = new RegExp(anchorPattern(src));
  } catch (error) {
    return `the pattern does not compile: ${error instanceof Error ? error.message : String(error)}`;
  }
  return refuseOverlappingRepeats(src) ?? { expression, groups: countGroups(src) };
}

function refuseUnknownFields(value: object, known: string[], where: string): Refusal {
  const unknown = Object.keys(value).find((key) => !known.includes(key));
  return unknown === undefined ? undefined : `${where} has a field no reader knows: ${unknown}`;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isStringList(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function refuseCacheRule(cache: unknown): Refusal {
  if (!isObject(cache)) return "a cache rule is an object";
  const unknown = refuseUnknownFields(
    cache,
    ["revalidate", "expire", "allowQuery", "tags", "bypassToken"],
    "a cache rule",
  );
  if (unknown) return unknown;
  const { revalidate, expire, allowQuery, tags, bypassToken } = cache;
  if (!Number.isInteger(revalidate) || (revalidate as number) < 1) {
    return "revalidate is a whole number of seconds, at least 1";
  }
  if (
    expire !== undefined &&
    (!Number.isInteger(expire) || (expire as number) <= (revalidate as number))
  ) {
    return "expire is a whole number of seconds longer than revalidate";
  }
  if (allowQuery !== undefined && (!isStringList(allowQuery) || allowQuery.includes(""))) {
    return "allowQuery is a list of parameter names, none of them empty";
  }
  if (tags !== undefined && (!isStringList(tags) || tags.includes(""))) {
    return "tags is a list of tags, none of them empty";
  }
  if (
    bypassToken !== undefined &&
    (typeof bypassToken !== "string" ||
      bypassToken.length < minBypassToken ||
      !bypassTokenCharacters.test(bypassToken))
  ) {
    return `bypassToken is at least ${minBypassToken} letters, digits, dashes or underscores`;
  }
  return undefined;
}

function refuseRoute(route: unknown): Refusal {
  if (!isObject(route)) return "a route is an object";
  const unknown = refuseUnknownFields(route, ["src", "function", "cache"], "a route");
  if (unknown) return unknown;
  if (typeof route.src !== "string") return "a route names a pattern";
  const pattern = compilePattern(route.src);
  if (typeof pattern === "string") return pattern;
  if (typeof route.function !== "string" || route.function === "")
    return "a route names a function";
  return route.cache === undefined ? undefined : refuseCacheRule(route.cache);
}

function refuseHeaderRule(rule: unknown): Refusal {
  if (!isObject(rule)) return "a header rule is an object";
  const unknown = refuseUnknownFields(rule, ["src", "headers"], "a header rule");
  if (unknown) return unknown;
  if (typeof rule.src !== "string") return "a header rule names a pattern";
  const pattern = compilePattern(rule.src);
  if (typeof pattern === "string") return pattern;
  if (!isObject(rule.headers) || Object.keys(rule.headers).length === 0) {
    return "a header rule sets at least one header";
  }
  for (const [name, value] of Object.entries(rule.headers)) {
    if (!headerName.test(name)) return `${JSON.stringify(name)} is not a header name`;
    if (typeof value !== "string" || unsendableHeaderValue.test(value)) {
      return `the value of ${name} is a string of tabs, visible ASCII and Latin-1 characters`;
    }
  }
  return undefined;
}

function refuseRedirect(rule: unknown): Refusal {
  if (!isObject(rule)) return "a redirect is an object";
  const unknown = refuseUnknownFields(rule, ["src", "location", "status"], "a redirect");
  if (unknown) return unknown;
  if (typeof rule.src !== "string") return "a redirect names a pattern";
  const pattern = compilePattern(rule.src);
  if (typeof pattern === "string") return pattern;
  if (!redirectStatuses.includes(rule.status as number)) {
    return `status ${String(rule.status)} is not 301, 302, 307 or 308`;
  }
  if (typeof rule.location !== "string" || rule.location === "")
    return "a redirect names a location";
  if (!absoluteLocation.test(rule.location) && !sameSiteLocation.test(rule.location)) {
    return "a location is a path that begins with one slash, or an http or https URL";
  }
  for (const read of rule.location.matchAll(groupReference)) {
    const group = Number(read[1]);
    if (group < 1 || group > pattern.groups) {
      return `the location reads $${read[1]}, and the pattern has ${pattern.groups} groups`;
    }
  }
  return undefined;
}

function refuseEach(items: unknown[], refuse: (item: unknown) => Refusal, what: string): Refusal {
  for (const [i, item] of items.entries()) {
    const refused = refuse(item);
    if (refused) return `${what} ${i}: ${refused}`;
  }
  return undefined;
}

function refuseBypassTokens(routes: Route[]): Refusal {
  const tokens = new Set(routes.flatMap((route) => route.cache?.bypassToken ?? []));
  return tokens.size > 1 ? "a table has one bypassToken, and its routes state more" : undefined;
}

const trailingSlashModes = ["never", "always", "ignore"];

function refuseTable(table: Record<string, unknown>): Refusal {
  const unknown = refuseUnknownFields(
    table,
    ["version", "basePath", "trailingSlash", "headers", "redirects", "routes", "fallback"],
    "the table",
  );
  if (unknown) return unknown;
  const { version, basePath, trailingSlash, headers, redirects, routes, fallback } = table;
  if (version !== 1)
    return `the table is version ${String(version)}, and this reader reads version 1`;
  if (
    typeof basePath !== "string" ||
    (basePath !== "" &&
      (!basePath.startsWith("/") || basePath.endsWith("/") || /[?#]/.test(basePath)))
  ) {
    return `basePath ${JSON.stringify(basePath)} is neither empty nor a path that starts with a slash, does not end with one and names no query or fragment`;
  }
  if (!trailingSlashModes.includes(trailingSlash as string)) {
    return `trailingSlash is ${JSON.stringify(trailingSlash)}, and it is never, always or ignore`;
  }
  if (!Array.isArray(headers) || !Array.isArray(redirects) || !Array.isArray(routes)) {
    return "the table states headers, redirects and routes, each a list, empty if need be";
  }
  if (!isObject(fallback) || typeof fallback.function !== "string" || fallback.function === "") {
    return "the fallback names no function";
  }
  const extra = refuseUnknownFields(fallback, ["function"], "the fallback");
  return (
    extra ??
    refuseEach(headers, refuseHeaderRule, "header rule") ??
    refuseEach(redirects, refuseRedirect, "redirect") ??
    refuseEach(routes, refuseRoute, "route") ??
    refuseBypassTokens(routes as Route[])
  );
}

export function readOcelRouteTable(table: unknown): OcelRouteTable {
  if (!isObject(table)) throw new Error("ocel: the route table is not an object");
  const refused = refuseTable(table);
  if (refused) throw new Error(`ocel: the route table is refused: ${refused}`);
  return table as unknown as OcelRouteTable;
}

export function parseOcelRouteTable(text: string): OcelRouteTable {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new Error("ocel: the route table is not JSON");
  }
  return readOcelRouteTable(parsed);
}

export interface CompiledRouteTable {
  table: OcelRouteTable;
  headers: RegExp[];
  redirects: RegExp[];
  routes: RegExp[];
}

function compileExpression(src: string): RegExp {
  return new RegExp(anchorPattern(src));
}

export function compileOcelRouteTable(table: OcelRouteTable): CompiledRouteTable {
  return {
    table,
    headers: table.headers.map((rule) => compileExpression(rule.src)),
    redirects: table.redirects.map((rule) => compileExpression(rule.src)),
    routes: table.routes.map((route) => compileExpression(route.src)),
  };
}

function readInsideBasePath(table: OcelRouteTable, pathname: string): string | undefined {
  if (pathname.length > longestMatchedPath) return undefined;
  const { basePath } = table;
  if (basePath === "") return pathname;
  if (pathname === basePath) return "/";
  return pathname.startsWith(`${basePath}/`) ? pathname.slice(basePath.length) : undefined;
}

function readGroups(match: RegExpExecArray): string[] {
  return match.slice(1).map((group) => group ?? "");
}

export interface RouteMatch {
  index: number;
  route: Route;
  groups: string[];
}

export function findRoute(compiled: CompiledRouteTable, pathname: string): RouteMatch | undefined {
  const inside = readInsideBasePath(compiled.table, pathname);
  if (inside === undefined) return undefined;
  for (const [index, expression] of compiled.routes.entries()) {
    const match = expression.exec(inside);
    if (match) return { index, route: compiled.table.routes[index]!, groups: readGroups(match) };
  }
  return undefined;
}

export function findCacheRule(
  compiled: CompiledRouteTable,
  pathname: string,
): CacheRule | undefined {
  return findRoute(compiled, pathname)?.route.cache;
}

export function findRedirect(
  compiled: CompiledRouteTable,
  pathname: string,
): { status: number; location: string } | undefined {
  const inside = readInsideBasePath(compiled.table, pathname);
  if (inside === undefined) return undefined;
  for (const [index, expression] of compiled.redirects.entries()) {
    const match = expression.exec(inside);
    if (!match) continue;
    const { location, status } = compiled.table.redirects[index]!;
    const groups = readGroups(match);
    const substituted = location.replace(
      groupReference,
      (_, group: string) => groups[Number(group) - 1] ?? "",
    );
    return {
      status,
      location: absoluteLocation.test(location) ? substituted : collapseLeadingSlashes(substituted),
    };
  }
  return undefined;
}

export function addRedirectQuery(location: string, query: string): string {
  const fragmentAt = location.indexOf("#");
  const target = fragmentAt === -1 ? location : location.slice(0, fragmentAt);
  if (target.includes("?")) return location;
  return target + query + location.slice(target.length);
}

export function findHeaders(
  compiled: CompiledRouteTable,
  pathname: string,
): Record<string, string> {
  const inside = readInsideBasePath(compiled.table, pathname);
  const found: Record<string, string> = {};
  if (inside === undefined) return found;
  for (const [index, expression] of compiled.headers.entries()) {
    if (!expression.test(inside)) continue;
    for (const [name, value] of Object.entries(compiled.table.headers[index]!.headers)) {
      found[name.toLowerCase()] = value;
    }
  }
  return found;
}

function collapseLeadingSlashes(location: string): string {
  return location.replace(leadingSlashes, "/");
}

export function findSlashRedirect(
  compiled: CompiledRouteTable,
  pathname: string,
): string | undefined {
  const { basePath, trailingSlash } = compiled.table;
  if (trailingSlash === "ignore") return undefined;
  if (pathname !== basePath && !pathname.startsWith(`${basePath}/`) && basePath !== "")
    return undefined;
  if (trailingSlash === "never") {
    if (!pathname.endsWith("/") || pathname === "/") return undefined;
    return collapseLeadingSlashes(pathname.replace(/\/+$/, "") || "/");
  }
  if (pathname.endsWith("/")) return undefined;
  const lastSegment = pathname.slice(pathname.lastIndexOf("/") + 1);
  return lastSegment.includes(".") ? undefined : collapseLeadingSlashes(`${pathname}/`);
}

export function formatCacheControl({ revalidate, expire }: CacheRule): string {
  return expire !== undefined && expire > revalidate
    ? `s-maxage=${revalidate}, stale-while-revalidate=${expire - revalidate}`
    : `s-maxage=${revalidate}`;
}
