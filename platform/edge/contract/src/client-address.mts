export const CLIENT_ADDRESS_HEADER = "x-ocel-client-address";

export function carryClientAddress(headers: Headers, client: string | null): void {
  headers.delete(CLIENT_ADDRESS_HEADER);
  if (client !== null) headers.set(CLIENT_ADDRESS_HEADER, client);
}
