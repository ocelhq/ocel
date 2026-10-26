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

export const tunedDatabase = defineTransform({
  gcp: {
    postgres: {
      instance: {
        settings: {
          tier: "db-custom-2-7680",
          availabilityType: "ZONAL",
          diskSize: 50,
          databaseFlags: [{ name: "log_min_duration_statement", value: "500" }],
          maintenanceWindow: { day: 7, hour: 3 },
          backupConfiguration: {
            startTime: "02:00",
            backupRetentionSettings: { retainedBackups: 14 },
          },
          ipConfiguration: {
            pscConfigs: [{ pscAutoConnections: [{ consumerNetwork: bindings.custom.network.id }] }],
          },
        },
      },
    },
  },
});

export const ownedDatabaseVersion = defineTransform({
  gcp: {
    postgres: {
      instance: {
        // @ts-expect-error ocel runs the version the database declares
        databaseVersion: "POSTGRES_16",
      },
    },
  },
});

export const ownedPublicAddress = defineTransform({
  gcp: {
    postgres: {
      instance: {
        settings: {
          // @ts-expect-error ocel keeps the instance's public address off
          ipConfiguration: { ipv4Enabled: true },
        },
      },
    },
  },
});

export const tunedStore = defineTransform({
  gcp: {
    kv: {
      instance: {
        replicaCount: 2,
        zoneDistributionConfig: { mode: "SINGLE_ZONE", zone: "europe-west1-b" },
        maintenancePolicy: {
          weeklyMaintenanceWindows: [{ day: "SUNDAY", startTime: { hours: 3 } }],
        },
        desiredAutoCreatedEndpoints: [{ network: "shared" }],
      },
    },
  },
});

export const ownedPersistence = defineTransform({
  gcp: {
    kv: {
      instance: {
        // @ts-expect-error append-only persistence is ocel's promise for a store
        persistenceConfig: { mode: "RDB" },
      },
    },
  },
});
