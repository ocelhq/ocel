import type {
  Request as ExpressRequest,
  RequestHandler,
  Response as ExpressResponse,
} from "express";
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
 * The Express binding of `uploader`. `middleware` receives the Express
 * `Request` the core reads from directly (a Node `IncomingMessage`); this only
 * narrows the request type.
 */
export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<ExpressRequest, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, ExpressRequest> {
  return coreUploader<TInput, TMetadata, ExpressRequest>(auth, upload);
}

async function sendResponse(
  res: ExpressResponse,
  webRes: Response,
): Promise<void> {
  res.status(webRes.status);
  webRes.headers.forEach((value, key) => res.setHeader(key, value));
  res.end(Buffer.from(await webRes.arrayBuffer()));
}

/**
 * The Express binding of `createRouteHandler`. Returns a `RequestHandler` for
 * both methods — mount it with `app.use(path, handler)`. The core reads the
 * Express `Request` directly (path + Host rebuild the URL; a body already
 * parsed by `express.json()` is read from `req.body`), and its `Response` is
 * written back onto the Express `Response`.
 */
export function createRouteHandler(
  bucket: Bucket,
  options?: RouteOptions,
): RequestHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket, options);
  return (req, res, next) => {
    const handler = req.method === "GET" ? GET : POST;
    handler(req)
      .then((webRes) => sendResponse(res, webRes))
      .catch(next);
  };
}
