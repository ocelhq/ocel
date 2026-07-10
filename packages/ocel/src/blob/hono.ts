import type { Context } from "hono";
import type { z } from "zod";
import type { Bucket } from "./bucket";
import {
  createRouteHandler as coreCreateRouteHandler,
  type RouteOptions,
} from "./route";
import { uploader as coreUploader } from "./uploader";
import type { ParsedInput, Uploader, UploaderAuth, UploaderUpload } from "./types";

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
 * The Hono binding of `uploader`. The route hands `middleware` the Hono
 * `Context`, so it can read headers, cookies, and `c.var`; this only narrows
 * the request type.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<Context, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, Context> {
  return coreUploader<TInput, TMetadata, Context>(auth, upload);
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
