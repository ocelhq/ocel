import { CLIENT_AUTHORIZATION_HEADER } from "@platform/edge-contract/client-authorization";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";

export function clientCertificateOriginFetch(binding: Fetcher): typeof fetch {
  return (async (input, init) => {
    const request = new Request(input as RequestInfo, init);
    const { protocol } = new URL(request.url);
    if (protocol !== "https:") {
      throw new Error(`ocel: refusing to present the client certificate over ${protocol}`);
    }
    const headers = new Headers(request.headers);
    headers.delete(CLIENT_AUTHORIZATION_HEADER);
    return dropEmptyBodySentinel(
      await binding.fetch(new Request(request, { headers, redirect: "manual" })),
    );
  }) as typeof fetch;
}
