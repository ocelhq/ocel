import { z } from "zod";

export const resolveResourcesSchema = z.object({
  projectId: z.string().min(1),
  resources: z
    .array(
      z.object({
        name: z.string().min(1),
        type: z.string().min(1),
        config: z.record(z.string(), z.unknown()).optional().default({}),
      }),
    )
    .min(1),
});

export type ResolveResourcesInput = z.infer<typeof resolveResourcesSchema>;
