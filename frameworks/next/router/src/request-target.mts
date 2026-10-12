const REQUEST_TARGET_ILLEGAL = /[[\]^|]/g;

export function encodeRequestTarget(pathname: string): string {
  return pathname.replace(
    REQUEST_TARGET_ILLEGAL,
    (character) => `%${character.charCodeAt(0).toString(16).toUpperCase()}`,
  );
}
