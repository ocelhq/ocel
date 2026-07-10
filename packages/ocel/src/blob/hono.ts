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
 * The Hono binding of `uploader`. The route hands `middleware` Hono's
 * underlying Web `Request` (`c.req.raw`); this only narrows the request type.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<Request, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, Request> {
  return coreUploader<TInput, TMetadata, Request>(auth, upload);
}

export type HonoRouteHandler = (c: Context) => Promise<Response>;

/**
 * The Hono binding of `createRouteHandler`. Returns a single handler covering
 * both methods — mount it with `app.on(["GET", "POST"], path, handler)`. It
 * unwraps Hono's Web `Request` from the context and returns the core `Response`
 * for Hono to send verbatim.
 */
export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): HonoRouteHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return (c) => (c.req.method === "GET" ? GET(c.req.raw) : POST(c.req.raw));
}
