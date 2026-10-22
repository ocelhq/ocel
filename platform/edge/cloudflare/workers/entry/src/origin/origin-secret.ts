import { CLIENT_AUTHORIZATION_HEADER } from "@platform/edge-contract/client-authorization";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";

export const ORIGIN_SECRET_HEADER = "x-ocel-origin-secret";

export interface OriginSecrets {
  secret: string;
  previous?: { secret: string; fingerprint: string };
}

export function presentedSecret(
  { secret, previous }: OriginSecrets,
  heldFingerprint: string | undefined,
): string {
  return previous !== undefined && heldFingerprint === previous.fingerprint
    ? previous.secret
    : secret;
}

export function originSecretFetch(secret: string): typeof fetch {
  return (async (input, init) => {
    const request = new Request(input as RequestInfo, init);
    const { protocol } = new URL(request.url);
    if (protocol !== "https:") {
      throw new Error(`ocel: refusing to present the origin secret over ${protocol}`);
    }
    const headers = new Headers(request.headers);
    headers.delete(CLIENT_AUTHORIZATION_HEADER);
    headers.set(ORIGIN_SECRET_HEADER, secret);
    return dropEmptyBodySentinel(
      await fetch(new Request(request, { headers, redirect: "manual" })),
    );
  }) as typeof fetch;
}
