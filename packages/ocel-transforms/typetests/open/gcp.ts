import { bindings, defineTransform } from "@ocel/transforms";
import type { LeafPaths } from "@ocel/transforms/gcp";
import type { cloudrunv2, pubsub, storage } from "@pulumi/gcp";
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

export const retainedTopic = defineTransform({
  gcp: {
    topic: {
      topic: {
        messageRetentionDuration: "86400s",
        kmsKeyName: bindings.custom.keys.topicKey,
        messageStoragePolicy: {
          allowedPersistenceRegions: ["europe-west1"],
          enforceInTransit: true,
        },
      },
    },
    task: { topic: { messageRetentionDuration: "3600s" } },
  },
});

export const ownedTopicSchema = defineTransform({
  gcp: {
    topic: {
      topic: {
        // @ts-expect-error ocel attaches no schema, and a schema is never patched on
        schemaSettings: { schema: "projects/acme/schemas/orders" },
      },
    },
  },
});

export const ownedTopicName = defineTransform({
  gcp: {
    task: {
      topic: {
        // @ts-expect-error ocel names the topic itself
        name: "mine",
      },
    },
  },
});

export const realTopicPath: LeafPaths<Unwrap<pubsub.TopicArgs>> =
  "messageStoragePolicy.enforceInTransit";

export const retainedAcknowledged = defineTransform({
  gcp: {
    topic: { subscription: { retainAckedMessages: true, messageRetentionDuration: "86400s" } },
  },
});

export const ownedPush = defineTransform({
  gcp: {
    task: {
      subscription: {
        // @ts-expect-error ocel pushes each message to its worker
        pushConfig: { pushEndpoint: "https://example.com" },
      },
    },
  },
});

export const deadLettered = defineTransform({
  gcp: {
    topic: {
      subscription: {
        // @ts-expect-error a message is redelivered until its run settles, never dead-lettered
        deadLetterPolicy: { deadLetterTopic: "projects/acme/topics/dead" },
      },
    },
  },
});

export const retriedJob = defineTransform({
  gcp: {
    task: {
      job: {
        description: "the nightly digest",
        retryConfig: { retryCount: 3, minBackoffDuration: "10s", maxBackoffDuration: "300s" },
      },
    },
  },
});

export const ownedSchedule = defineTransform({
  gcp: {
    task: {
      job: {
        // @ts-expect-error the cron comes from the task
        schedule: "0 * * * *",
      },
    },
  },
});

export const topicJob = defineTransform({
  gcp: {
    topic: {
      // @ts-expect-error only a task fires on a cron
      job: { description: "never fires" },
    },
  },
});

export const tunedService = defineTransform({
  gcp: {
    function: {
      service: {
        template: {
          containers: [
            {
              resources: {
                limits: { cpu: "4", memory: "16Gi", "nvidia.com/gpu": "1" },
                cpuIdle: false,
                startupCpuBoost: true,
              },
            },
          ],
          scaling: { minInstanceCount: 1, maxInstanceCount: 10 },
          maxInstanceRequestConcurrency: 20,
          executionEnvironment: "EXECUTION_ENVIRONMENT_GEN2",
          sessionAffinity: true,
          timeout: "900s",
          nodeSelector: { accelerator: "nvidia-l4" },
          encryptionKey: bindings.custom.keys.serviceKey,
          serviceAccount: bindings.custom.identity.webAccount,
          vpcAccess: {
            egress: "ALL_TRAFFIC",
            networkInterfaces: [{ network: "apps", subnetwork: "apps-eu", tags: ["nat"] }],
          },
        },
        binaryAuthorization: { useDefault: true },
      },
    },
  },
});

export const ownedImage = defineTransform({
  gcp: {
    container: {
      service: {
        template: {
          containers: [
            {
              // @ts-expect-error ocel runs the image the deploy built
              image: "nginx",
            },
          ],
        },
      },
    },
  },
});

export const ownedIngress = defineTransform({
  gcp: {
    container: {
      service: {
        // @ts-expect-error ocel sets the ingress the edge needs
        ingress: "INGRESS_TRAFFIC_ALL",
      },
    },
  },
});

export const workerTimeout = defineTransform({
  gcp: {
    worker: {
      service: {
        template: {
          // @ts-expect-error a worker's timeout is the push deadline ocel keeps
          timeout: "60s",
        },
      },
    },
  },
});

export const realServicePath: LeafPaths<Unwrap<cloudrunv2.ServiceArgs>> =
  "template.containers.resources.limits.nvidia.com/gpu";

export const tierNetwork = defineTransform({
  gcp: {
    network: {
      network: { mtu: 1500 },
      subnetwork: {
        ipCidrRange: "10.96.0.0/20",
        privateIpGoogleAccess: true,
        logConfig: { flowSampling: 0.25 },
        secondaryIpRanges: [{ rangeName: "pods", ipCidrRange: "10.100.0.0/20" }],
      },
      connectionPolicy: { pscConfig: { limit: "20" } },
    },
  },
});

export const tierNetworkFromBinding = defineTransform({
  gcp: {
    network: {
      connectionPolicy: {
        pscConfig: {
          // @ts-expect-error the network belongs to every project of the tier, so no project's binding fills it
          limit: bindings.custom.limits.connections,
        },
      },
    },
  },
});

export const ownedRouting = defineTransform({
  gcp: {
    network: {
      network: {
        // @ts-expect-error ocel routes the tier's network regionally
        routingMode: "GLOBAL",
      },
    },
  },
});
