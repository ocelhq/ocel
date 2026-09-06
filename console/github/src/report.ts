import { z } from "zod";

export const deployResultSchema = z.looseObject({
  schemaVersion: z.literal(1),
  slug: z.string(),
  environment: z.looseObject({
    class: z.string(),
    identity: z.string().optional(),
  }),
  promotionId: z.string(),
  tag: z.string().optional(),
  apps: z.array(
    z.looseObject({
      name: z.string(),
      buildId: z.string().optional(),
      deploymentId: z.string().optional(),
      urls: z.array(z.string()),
    }),
  ),
  deployedAt: z.string(),
});

export const phaseSchema = z.enum(["started", "deployed", "failed", "removed"]);

export const reportSchema = z
  .object({
    repo: z.string().regex(/^[^/\s]+\/[^/\s]+$/),
    pr: z.number().int().positive(),
    sha: z.string().regex(/^[0-9a-f]{40}$/),
    ref: z.string().min(1),
    run_url: z.url(),
    phase: phaseSchema,
    result: deployResultSchema.optional(),
    error: z.string().optional(),
  })
  .refine((report) => report.phase !== "deployed" || report.result !== undefined, {
    message: "result is required when phase is deployed",
    path: ["result"],
  });

export type DeployResult = z.infer<typeof deployResultSchema>;
export type Phase = z.infer<typeof phaseSchema>;
export type Report = z.infer<typeof reportSchema>;
