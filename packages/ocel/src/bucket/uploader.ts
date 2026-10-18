import type { StandardSchemaV1 } from "@standard-schema/spec";
import type {
  ParsedInput,
  Uploader,
  UploaderAuth,
  UploaderUpload,
  UploadRequest,
} from "./types.js";

export function uploader<
  TInput extends StandardSchemaV1 | undefined = undefined,
  TMetadata = unknown,
  TReq = UploadRequest,
>(
  auth: UploaderAuth<TReq, TInput, TMetadata>,
  upload: UploaderUpload<TMetadata> = {},
): Uploader<ParsedInput<TInput>, TMetadata, TReq> {
  return {
    auth: auth as UploaderAuth<TReq, StandardSchemaV1 | undefined, TMetadata>,
    upload,
  };
}
