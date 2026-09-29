export const CLIENT_AUTHORIZATION_HEADER = "x-ocel-client-authorization";

export function carryClientAuthorization(headers: Headers): void {
  headers.delete(CLIENT_AUTHORIZATION_HEADER);
  const authorization = headers.get("authorization");
  if (authorization !== null) headers.set(CLIENT_AUTHORIZATION_HEADER, authorization);
}
