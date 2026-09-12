import {
  APP_OUTCOMES,
  COMPUTE_KINDS,
  DEPLOYMENT_KINDS,
  DEPLOYMENT_OUTCOMES,
  ENVIRONMENT_CLASSES,
  FRAMEWORKS,
  RESOURCE_TYPES,
  STAGE_STATUSES,
  TRIGGER_KINDS,
  VARIABLE_CLASSES,
} from "@console/db/schema";
import { z } from "zod";

const variableSchema = z.object({
  key: z.string().min(1).max(256),
  class: z.enum(VARIABLE_CLASSES),
  folder: z.string().max(512).optional(),
  description: z.string().max(1000).optional(),
});

const appSchema = z.object({
  name: z.string().min(1).max(63),
  folder: z.string().max(512).optional(),
  runtime: z.object({
    name: z.string().min(1).max(32),
    arch: z.string().max(32).optional(),
  }),
  framework: z.enum(FRAMEWORKS).optional(),
  compute: z.enum(COMPUTE_KINDS),
  deploymentId: z.string().max(256).optional(),
  buildId: z.string().max(256).optional(),
  urls: z.array(z.url()).max(100),
  hostnames: z.array(z.string().min(1).max(253)).max(100).default([]),
  healthPath: z.string().max(512).optional(),
  outcome: z.enum(APP_OUTCOMES),
  error: z.string().max(4000).optional(),
  variables: z.array(variableSchema).max(500),
});

const grantSchema = z.object({
  verb: z.string().max(64).optional(),
  actions: z.array(z.string().min(1).max(128)).max(100),
});

const resourceSchema = z.object({
  name: z.string().min(1).max(128),
  type: z.enum(RESOURCE_TYPES),
  binding: z.object({
    name: z.string().min(1).max(128),
    source: z.string().max(512).optional(),
    propertyKeys: z.array(z.string().min(1).max(256)).max(200),
    grants: z.array(grantSchema).max(100),
  }),
});

const usageSchema = z.object({
  app: z.string().min(1).max(63),
  resource: z.string().min(1).max(128),
  files: z.array(z.string().max(512)).max(200),
});

const stageSchema = z.object({
  name: z.string().min(1).max(128),
  app: z.string().max(63).optional(),
  startedAt: z.iso.datetime({ offset: true }),
  finishedAt: z.iso.datetime({ offset: true }),
  status: z.enum(STAGE_STATUSES),
  error: z.string().max(4000).optional(),
  log: z.array(z.string().max(2000)).max(200).default([]),
});

const triggerSchema = z.object({
  kind: z.enum(TRIGGER_KINDS),
  actor: z.string().max(256).optional(),
  ci: z
    .object({
      provider: z.string().min(1).max(64),
      runId: z.string().max(256).optional(),
      url: z.url().optional(),
    })
    .optional(),
});

const gitSchema = z.object({
  sha: z.string().min(7).max(64),
  branch: z.string().max(256).optional(),
  message: z.string().max(1000).optional(),
  author: z.string().max(256).optional(),
  dirty: z.boolean(),
});

const PROMOTION_KINDS = new Set(["deploy", "preview-up", "rollback"]);

export const deploymentRecordSchema = z
  .object({
    schemaVersion: z.literal(2),
    runId: z.string().min(1).max(128),
    kind: z.enum(DEPLOYMENT_KINDS),
    promotionId: z.string().min(1).max(128).optional(),
    tag: z.string().max(128).optional(),
    startedAt: z.iso.datetime({ offset: true }).optional(),
    deployedAt: z.iso.datetime({ offset: true }),
    outcome: z.enum(DEPLOYMENT_OUTCOMES),
    error: z.string().max(4000).optional(),
    environment: z.object({
      class: z.enum(ENVIRONMENT_CLASSES),
      identity: z.string().max(128).optional(),
    }),
    provider: z.object({
      name: z.string().min(1).max(64),
      region: z.string().max(64).optional(),
    }),
    edge: z.object({ kind: z.string().min(1).max(64) }).optional(),
    trigger: triggerSchema.default({ kind: "cli" }),
    git: gitSchema.optional(),
    cliVersion: z.string().max(64).optional(),
    trace: z.array(stageSchema).max(500).default([]),
    apps: z.array(appSchema).max(100),
    resources: z.array(resourceSchema).max(200),
    usages: z.array(usageSchema).max(2000),
  })
  .superRefine((record, ctx) => {
    if (PROMOTION_KINDS.has(record.kind) && record.outcome === "succeeded" && !record.promotionId) {
      ctx.addIssue({
        code: "custom",
        path: ["promotionId"],
        message: `A succeeded ${record.kind} names the promotion it made active`,
      });
    }
    const apps = new Set(record.apps.map((app) => app.name));
    const resources = new Set(record.resources.map((resource) => resource.name));
    record.usages.forEach((usage, position) => {
      if (!apps.has(usage.app)) {
        ctx.addIssue({
          code: "custom",
          path: ["usages", position, "app"],
          message: `Unknown app "${usage.app}"`,
        });
      }
      if (!resources.has(usage.resource)) {
        ctx.addIssue({
          code: "custom",
          path: ["usages", position, "resource"],
          message: `Unknown resource "${usage.resource}"`,
        });
      }
    });
    record.trace.forEach((stage, position) => {
      if (stage.app && !apps.has(stage.app)) {
        ctx.addIssue({
          code: "custom",
          path: ["trace", position, "app"],
          message: `Unknown app "${stage.app}"`,
        });
      }
    });
  });

export type DeploymentRecordInput = z.infer<typeof deploymentRecordSchema>;
