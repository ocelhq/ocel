import { z } from "zod";
import { signedFileSchema } from "../signing";
import { withinNamespace } from "../store";

export const presignUploadSchema = z.object({
  projectId: z.string().min(1),
  bucket: z.string().min(1),
  files: z
    .array(
      signedFileSchema.extend({
        key: z.string().min(1).refine(withinNamespace, "key must stay under its namespace"),
      }),
    )
    .min(1),
  metadata: z.string(),
  contentDisposition: z.string().optional().default(""),
  callbackBaseUrl: z.string().min(1),
});

export type PresignUploadInput = z.infer<typeof presignUploadSchema>;
