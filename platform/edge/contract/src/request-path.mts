const absoluteUrlOrigin = /^[a-z][a-z\d+.-]*:\/\/[^/]*/i;

const repeatedSlashOrBackslash = /(\\|\/\/)/;

export function isAbsoluteUrl(url: string): boolean {
  return absoluteUrlOrigin.test(url);
}

export function readPathAndQuery(url: string): string {
  return url.replace(absoluteUrlOrigin, "");
}

export function readRawPath(url: string): string {
  const pathAndQuery = readPathAndQuery(url);
  const queryIndex = pathAndQuery.indexOf("?");
  return queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
}

export function encodeForwardedSearch(search: string): string {
  const first = search.indexOf("?");
  if (first === -1) return search;
  return search.slice(0, first + 1) + search.slice(first + 1).replaceAll("?", "%3F");
}

export function needsSlashNormalization(pathWithoutQuery: string): boolean {
  return repeatedSlashOrBackslash.test(pathWithoutQuery);
}

export function normalizeRepeatedSlashes(pathAndQuery: string): string {
  const queryIndex = pathAndQuery.indexOf("?");
  const path = queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
  const query = queryIndex === -1 ? "" : pathAndQuery.slice(queryIndex);
  const normalized = path.replace(/\\/g, "/").replace(/\/\/+/g, "/");
  return normalized + query;
}
