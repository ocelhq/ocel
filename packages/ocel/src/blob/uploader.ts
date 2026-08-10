import type { z } from "zod";
import type {
  BlobRequest,
  ParsedInput,
  Uploader,
  UploaderAuth,
  UploaderUpload,
} from "./types.js";

export function uploader<
  TInput extends z.ZodType | undefined = undefined,
  TMetadata = unknown,
  TReq = BlobRequest,
>(
  auth: UploaderAuth<TReq, TInput, TMetadata>,
  upload: UploaderUpload<TMetadata> = {},
): Uploader<ParsedInput<TInput>, TMetadata, TReq> {
  return {
    auth: auth as UploaderAuth<TReq, z.ZodType | undefined, TMetadata>,
    upload,
  };
}
