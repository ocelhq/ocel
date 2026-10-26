import { CLIENT_ADDRESS_HEADER } from "@platform/edge-contract/client-address";
import { CLIENT_AUTHORIZATION_HEADER } from "@platform/edge-contract/client-authorization";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";

export const frontedFetch: typeof fetch = (async (input, init) => {
  const request = new Request(input as RequestInfo, init);
  const headers = new Headers(request.headers);
  headers.delete(CLIENT_AUTHORIZATION_HEADER);
  const client = headers.get(CLIENT_ADDRESS_HEADER);
  if (client !== null) headers.set("cf-connecting-ip", client);
  return dropEmptyBodySentinel(
    await fetch(new Request(request, { headers, redirect: "manual", cache: "no-store" })),
  );
}) as typeof fetch;

export function readFrontedOrigin(host: string): string {
  return `https://${host.toLowerCase().split(":", 1)[0]}`;
}
