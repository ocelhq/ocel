import type { StandardSchemaV1 } from "@standard-schema/spec";
import type {
  Request as ExpressRequest,
  Response as ExpressResponse,
  RequestHandler,
} from "express";
import type { Bucket } from "./bucket.js";
import { createRouteHandler as coreCreateRouteHandler } from "./route.js";
import type { ParsedInput, Uploader, UploaderAuth, UploaderUpload } from "./types.js";
import { uploader as coreUploader } from "./uploader.js";

export { Bucket, type BucketOptions, bucket } from "./bucket.js";
export type {
  CompletedFile,
  FileInfo,
  Limits,
  PathConfig,
  Uploader,
} from "./types.js";

export function uploader<
  TInput extends StandardSchemaV1 | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<ExpressRequest, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, ExpressRequest> {
  return coreUploader<TInput, TMetadata, ExpressRequest>(auth, upload);
}

async function sendResponse(res: ExpressResponse, webRes: Response): Promise<void> {
  res.status(webRes.status);
  webRes.headers.forEach((value, key) => {
    res.setHeader(key, value);
  });
  res.end(Buffer.from(await webRes.arrayBuffer()));
}

export function createRouteHandler(bucket: Bucket): RequestHandler {
  const { GET, POST } = coreCreateRouteHandler(bucket);
  return (req, res, next) => {
    if (req.originalUrl) req.url = req.originalUrl;
    const handler = req.method === "GET" ? GET : POST;
    handler(req)
      .then((webRes) => sendResponse(res, webRes))
      .catch(next);
  };
}
