import {
  encodeForwardedSearch,
  needsSlashNormalization,
  normalizeRepeatedSlashes,
  readPathAndQuery,
  readRawPath,
} from "@platform/edge-contract/request-path";

export interface NodeOriginDeps {
  app: string;
  urls: string[];
  originFetch?: typeof fetch;
}

export function nodeOrigin(deps: NodeOriginDeps): (request: Request) => Promise<Response> {
  const { app, urls, originFetch } = deps;

  return async (request) => {
    if (urls.length !== 1) return noSingleFunctionUrl(app, urls.length);
    if (!originFetch) return noOriginReach(app);

    if (needsSlashNormalization(readRawPath(request.url))) {
      return new Response(null, {
        status: 308,
        headers: { location: normalizeRepeatedSlashes(readPathAndQuery(request.url)) },
      });
    }

    const url = new URL(request.url);
    const headers = new Headers(request.headers);
    headers.set("x-forwarded-host", url.host);
    headers.set("x-forwarded-proto", url.protocol.replace(/:$/, ""));

    return originFetch(
      new Request(new URL(url.pathname + encodeForwardedSearch(url.search), urls[0]), {
        method: request.method,
        headers,
        body: request.body,
        redirect: "manual",
      }),
    );
  };
}

function noSingleFunctionUrl(app: string, count: number): Response {
  return plain(
    count === 0
      ? `${app} recorded neither a function URL nor a container origin to reach.`
      : `${app} published ${count} function URLs; a node app is served by exactly one.`,
  );
}

function noOriginReach(app: string): Response {
  return plain(
    `No way to reach the origin is bound to this worker; ${app} refuses a request that carries no signature or origin secret.`,
  );
}

function plain(body: string): Response {
  return new Response(body, {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}
