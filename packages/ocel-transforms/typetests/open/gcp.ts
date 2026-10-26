import { bindings, defineTransform } from "@ocel/transforms";
import type { LeafPaths } from "@ocel/transforms/gcp";
import type { storage } from "@pulumi/gcp";
import type { Unwrap } from "@pulumi/pulumi";

export const tuned = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        storageClass: "NEARLINE",
        versioning: { enabled: true },
        softDeletePolicy: { retentionDurationSeconds: 0 },
        retentionPolicy: { retentionPeriod: "86400" },
        encryption: { defaultKmsKeyName: bindings.custom.keys.bucketKey },
        logging: { logBucket: "acme-logs", logObjectPrefix: "uploads" },
        lifecycleRules: [
          { action: { type: "SetStorageClass", storageClass: "COLDLINE" }, condition: { age: 90 } },
        ],
      },
    },
  },
});

export const ownedName = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        // @ts-expect-error ocel names the bucket itself
        name: "mine",
      },
    },
  },
});

export const ownedAccess = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        // @ts-expect-error ocel keeps public access prevention enforced
        publicAccessPrevention: "inherited",
      },
    },
  },
});

export const ownedLabels = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        // @ts-expect-error labels come from the rule's tags
        labels: { team: "web" },
      },
    },
  },
});

export const untranslatedCondition = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        lifecycleRules: [
          {
            action: { type: "Delete" },
            // @ts-expect-error ocel has no translation for a size condition
            condition: { sizeAboveBytes: 1 },
          },
        ],
      },
    },
  },
});

export const lockedRetention = defineTransform({
  gcp: {
    bucket: {
      bucket: {
        // @ts-expect-error a locked retention policy is never patched
        retentionPolicy: { retentionPeriod: "86400", isLocked: true },
      },
    },
  },
});

export const realPath: LeafPaths<Unwrap<storage.BucketArgs>> = "versioning.enabled";

// @ts-expect-error a path is checked against the args @pulumi/gcp takes
export const madeUpPath: LeafPaths<Unwrap<storage.BucketArgs>> = "versioning.on";
