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

export function uploader<
  TInput extends StandardSchemaV1 | undefined = undefined,
  TMetadata = unknown,
>(
  auth: UploaderAuth<NextRequest, TInput, TMetadata>,
  upload?: UploaderUpload<TMetadata>,
): Uploader<ParsedInput<TInput>, TMetadata, NextRequest> {
  return coreUploader<TInput, TMetadata, NextRequest>(auth, upload);
}

export interface NextRouteHandlers {
  GET: (req: NextRequest) => Promise<Response>;
  POST: (req: NextRequest) => Promise<Response>;
}

export function createRouteHandler(bucket: Bucket): NextRouteHandlers {
  const { GET, POST } = coreCreateRouteHandler(bucket);
  return { GET, POST };
}
