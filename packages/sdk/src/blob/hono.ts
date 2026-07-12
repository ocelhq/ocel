import type { Context } from "hono";
import type { z } from "zod";
import type { Bucket } from "./bucket";
import {
  createRouteHandler as coreCreateRouteHandler,
  type RouteOptions,
} from "./route";
import { uploader as coreUploader } from "./uploader";
import type {
  MaybePromise,
  ParsedInput,
  Uploader,
  UploaderUpload,
} from "./types";

export { bucket, Bucket, type BucketOptions } from "./bucket";
export type { RouteOptions } from "./route";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types";

/**
 * The Hono binding of an uploader's auth. `middleware` receives the Hono
 * `Context` as `c` (not `req`), so it reads the request the Hono way -
 * `c.req.header(...)`, `c.get(...)`, `c.var` - with no `req.req` awkwardness.
 */
export interface HonoUploaderAuth<
  TInput extends z.ZodType | undefined,
  TMetadata,
> {
  input?: TInput;
  middleware: (ctx: {
    c: Context;
    input: ParsedInput<TInput>;
  }) => MaybePromise<TMetadata>;
}

/**
 * The Hono binding of `uploader`. Identical to the core except `middleware`
 * takes the Hono `Context` as `c`.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
>(
  auth: HonoUploaderAuth<TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, Context> {
  return coreUploader<TInput, TMetadata, Context>(
    {
      input: auth.input,
      // The core passes the Context as `req`; hand it to the user as `c`.
      middleware: ({ req, input }) => auth.middleware({ c: req, input }),
    },
    upload,
  );
}

export type HonoRouteHandler = (c: Context) => Promise<Response>;

/**
 * The Hono binding of `createRouteHandler`. Returns a single handler covering
 * both methods — mount it with `app.on(["GET", "POST"], path, handler)`. The
 * core reads the URL and body from Hono's underlying Web `Request` (`c.req.raw`)
 * while `middleware` receives the full `Context`; the core `Response` is sent
 * verbatim.
 */
export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): HonoRouteHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return (c) => (c.req.method === "GET" ? GET(c.req.raw) : POST(c.req.raw, c));
}
