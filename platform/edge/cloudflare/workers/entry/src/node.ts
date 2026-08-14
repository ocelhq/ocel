import { originBodyBytes, type OriginBodyBudget } from "./index";
import { needsSlashNormalization, normalizeRepeatedSlashes } from "./trailing-slash";

export interface NodeOriginDeps {
  app: string;
  functionUrls: Record<string, string>;
  originFetch?: typeof fetch;
  originBodyBudget?: OriginBodyBudget;
}

export function nodeOrigin(
  deps: NodeOriginDeps,
): (request: Request) => Promise<Response> {
  const urls = Object.values(deps.functionUrls);
  const { app, originFetch, originBodyBudget: budget } = deps;

  return async (request) => {
    if (urls.length !== 1) return noSingleFunctionUrl(app, urls.length);
    if (!originFetch) return noOriginSigner(app);

    const pathAndQuery = request.url.replace(/^[a-z][a-z\d+.-]*:\/\/[^/]*/i, "");
    const queryIndex = pathAndQuery.indexOf("?");
    const rawPath = queryIndex === -1 ? pathAndQuery : pathAndQuery.slice(0, queryIndex);
    if (needsSlashNormalization(rawPath)) {
      return new Response(null, {
        status: 308,
        headers: { location: normalizeRepeatedSlashes(pathAndQuery) },
      });
    }

    const url = new URL(request.url);
    const headers = new Headers(request.headers);
    headers.set("x-forwarded-host", url.host);
    headers.set("x-forwarded-proto", url.protocol.replace(/:$/, ""));

    let body: BodyInit | null = request.body;
    if (budget && request.body) {
      const contentType = request.headers.get("content-type");
      const declared = Number(request.headers.get("content-length"));
      if (Number.isFinite(declared) && declared > 0) {
        if (originBodyBytes(declared, contentType, budget.encoding) > budget.maxBytes) {
          return payloadTooLarge();
        }
      } else {
        const buffered = await request.arrayBuffer();
        if (
          originBodyBytes(buffered.byteLength, contentType, budget.encoding) > budget.maxBytes
        ) {
          return payloadTooLarge();
        }
        body = buffered;
      }
    }

    return originFetch(
      new Request(new URL(url.pathname + url.search, urls[0]), {
        method: request.method,
        headers,
        body,
        redirect: "manual",
      }),
    );
  };
}

function payloadTooLarge(): Response {
  return new Response(null, { status: 413 });
}

function noSingleFunctionUrl(app: string, count: number): Response {
  return plain(
    count === 0
      ? `No function URL for ${app}.`
      : `${app} published ${count} function URLs; a node app is served by exactly one.`,
  );
}

function noOriginSigner(app: string): Response {
  return plain(
    `No origin signing credentials are bound to this worker; ${app} would answer 403 to an unsigned request.`,
  );
}

function plain(body: string): Response {
  return new Response(body, {
    status: 502,
    headers: { "content-type": "text/plain; charset=utf-8" },
  });
}
