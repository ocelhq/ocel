import { CLIENT_AUTHORIZATION_HEADER } from "@platform/edge-contract/client-authorization";
import { dropEmptyBodySentinel } from "@platform/edge-contract/empty-body";
import type { Env } from "./env";
import { edgeOriginFetch } from "./signing";

export type OriginEnv = Pick<
  Env,
  "OCEL_EDGE_ACCESS_KEY_ID" | "OCEL_EDGE_SECRET_KEY" | "OCEL_ORIGIN_CLIENT_CERTIFICATE"
>;

export function clientCertificateOriginFetch(binding: Fetcher): typeof fetch {
  return (async (input, init) => {
    const request = new Request(input as RequestInfo, init);
    const { protocol } = new URL(request.url);
    if (protocol !== "https:") {
      throw new Error(`ocel: refusing to present the client certificate over ${protocol}`);
    }
    const headers = new Headers(request.headers);
    headers.delete(CLIENT_AUTHORIZATION_HEADER);
    const hasBody = request.method !== "GET" && request.method !== "HEAD";
    const body = hasBody ? await request.arrayBuffer() : undefined;
    return dropEmptyBodySentinel(
      await binding.fetch(
        new Request(request.url, {
          method: request.method,
          headers,
          body,
          redirect: "manual",
        }),
      ),
    );
  }) as typeof fetch;
}

export function originFetchFor(env: OriginEnv): typeof fetch | undefined {
  const certificate = env.OCEL_ORIGIN_CLIENT_CERTIFICATE;
  if (certificate) {
    if (env.OCEL_EDGE_ACCESS_KEY_ID || env.OCEL_EDGE_SECRET_KEY) {
      throw new Error(
        "ocel: this worker is bound to both AWS signing keys and a client certificate; an origin is reached one way",
      );
    }
    return clientCertificateOriginFetch(certificate);
  }
  return edgeOriginFetch(env.OCEL_EDGE_ACCESS_KEY_ID, env.OCEL_EDGE_SECRET_KEY);
}
