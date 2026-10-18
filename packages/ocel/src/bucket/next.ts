import "./server-only.js";
import type { StandardSchemaV1 } from "@standard-schema/spec";
import type { NextRequest } from "next/server";
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

/**
 * Declares a browser-upload flow: `auth` decides from the Next request who may upload and
 * returns the metadata the upload carries, and `upload` bounds what is accepted and where
 * it is stored.
 */
export function uploader<
  TInput extends StandardSchemaV1 | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<NextRequest, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, NextRequest> {
  return coreUploader<TInput, TMetadata, NextRequest>(auth, upload);
}

/** The handlers a Next route file exports as `GET` and `POST`. */
export interface NextRouteHandlers {
  /** Serves the browser upload client's checks that an upload finished. */
  GET: (req: NextRequest) => Promise<Response>;
  /** Serves the browser upload client's requests to start and finish an upload. */
  POST: (req: NextRequest) => Promise<Response>;
}

/** The route handlers that serve `bucket`'s uploaders to the browser upload client. */
export function createRouteHandler(bucket: Bucket): NextRouteHandlers {
  const { GET, POST } = coreCreateRouteHandler(bucket);
  return { GET, POST };
}
