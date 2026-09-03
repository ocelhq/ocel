const REQUEST_TARGET_ILLEGAL = /[[\]^|]/g;

export function encodeRequestTarget(pathname: string): string {
  return pathname.replace(
    REQUEST_TARGET_ILLEGAL,
    (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`,
  );
}

export function encodeForwardedSearch(search: string): string {
  const first = search.indexOf("?");
  if (first === -1) return search;
  return (
    search.slice(0, first + 1) + search.slice(first + 1).replaceAll("?", "%3F")
  );
}
