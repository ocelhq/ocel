/**
 * Welcome to Cloudflare Workers! This is your first worker.
 *
 * - Run `npm run dev` in your terminal to start a development server
 * - Open a browser tab at http://localhost:8787/ to see your worker in action
 * - Run `npm run deploy` to publish your worker
 *
 * Bind resources to your worker in `wrangler.jsonc`. After adding bindings, a type definition for the
 * `Env` object can be regenerated with `npm run cf-typegen`.
 *
 * Learn more at https://developers.cloudflare.com/workers/
 */
import { resolveRoutes } from "@next/routing";

interface Env {
  ASSETS: Fetcher;
  FUNCTION_URLS: string;
}

export default {
  async fetch(request, env, ctx): Promise<Response> {
    const url = new URL(request.url);
    const specifier = "./routing-manifest.json";
    const manifest = await import(specifier);

    const result = await resolveRoutes({
      url: new URL(request.url),
      buildId: manifest.buildId,
      basePath: manifest.basePath,
      i18n: undefined,
      headers: request.headers,
      requestBody: request.body as ReadableStream,
      pathnames: manifest.pathnames,
      routes: manifest.routes,

      // TODO: invoke user-defined middleware
      invokeMiddleware: async (mwCtx) => {
        return {};
      },
    });

    if (result.middlewareResponded) {
      return new Response(null, { status: result.status ?? 200 });
    }
    if (result.redirect) {
      return Response.redirect(
        result.redirect.url.toString(),
        result.redirect.status,
      );
    }
    if (result.externalRewrite) {
      return fetch(new Request(result.externalRewrite, request));
    }
    if (!result.resolvedPathname) {
      return new Response("Not Found", { status: 404 });
    }

    const concrete = result.invocationTarget?.pathname;
    const pre = concrete ? manifest.dispatch[concrete] : null;

    // TODO: server pre-render, trigger revalidate if necessary
    if (pre) {
    }

    const target = manifest.dispatch[result.resolvedPathname];
    const forwardUrl = result.invocationTarget;

    if (!target) {
      return new Response("Not Found", { status: 404 });
    }

    switch (target.kind) {
      case "static": {
        // Workers Assets serves _next/static and other truly-static files.
        // Use the ORIGINAL request URL so the asset path matches.
        const res = await env.ASSETS.fetch(request);
        return res.status === 404
          ? new Response("Not Found", { status: 404 })
          : res;
      }

      case "lambda": {
        const fnUrls = JSON.parse(env.FUNCTION_URLS) as Record<string, string>;
        const fnUrl = fnUrls[target.id];
        if (!fnUrl) {
          return new Response(`No function URL for ${target.id}`, {
            status: 502,
          });
        }

        // Forward the (possibly rewritten) invocation target path+query.
        const forwardUrl = result.invocationTarget
          ? new URL(result.invocationTarget.pathname + url.search, fnUrl)
          : new URL(url.pathname + url.search, fnUrl);

        // Function URL takes a normal HTTP request; AWS wraps it into the v2
        // event our Go bootstrap parses. Just proxy the request through.
        const proxied = new Request(forwardUrl, {
          method: request.method,
          headers: request.headers,
          body: request.body,
          redirect: "manual",
        });

        return fetch(proxied);
      }

      case "edge": {
        // TODO: edge functions run in-Worker. For the demo, treat like lambda
        // or import the compiled edge entry by target.entryKey and invoke it.
        return new Response("Edge runtime not wired yet", { status: 501 });
      }

      default:
        return new Response("Not Found", { status: 404 });
    }

    return new Response("Hello World!");
  },
} satisfies ExportedHandler<Env>;
