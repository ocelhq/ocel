import type {
  cloudrunv2,
  cloudscheduler,
  compute,
  memorystore,
  networkconnectivity,
  pubsub,
  sql,
  storage,
} from "@pulumi/gcp";
import type { Unwrap } from "@pulumi/pulumi";
import type { Patch } from "../patch.js";

/**
 * One key per Google Cloud resource the GCP provider constructs for an ocel
 * resource, typed from the `@pulumi/gcp` args for the same resource.
 */
export interface GcpResourceArgs {
  /**
   * A bucket's Cloud Storage bucket. Lifecycle rules are added after the ones
   * ocel keeps to clean up abandoned uploads, which stay.
   */
  bucket: {
    bucket: storage.BucketArgs;
  };
  /**
   * A postgres database's Cloud SQL instance. ocel names it and keeps its
   * region, Postgres version, private endpoint, encrypted connections, user
   * and database. A placement names the one network its endpoint is made in,
   * which needs a Cloud SQL service connection policy in the deploy's region;
   * an endpoint never moves to another network once made.
   */
  postgres: {
    instance: sql.DatabaseInstanceArgs;
  };
  /**
   * A kv store's Memorystore instance. ocel sizes its node from the store's
   * memory and keeps its version, eviction, token auth, TLS and append-only
   * persistence. A placement names the one network its endpoint is made in,
   * which needs a Memorystore service connection policy in the deploy's
   * region; an endpoint never moves to another network once made.
   */
  kv: {
    instance: memorystore.InstanceArgs;
  };
  /**
   * A topic: its Pub/Sub topic, and the push subscription each consumer's
   * worker makes. A rule's tags become labels on both.
   */
  topic: {
    /**
     * The Pub/Sub topic. A `kmsKeyName` must already let the project's Pub/Sub
     * service agent (`service-<project number>@gcp-sa-pubsub.iam.gserviceaccount.com`)
     * encrypt and decrypt with it: ocel grants nothing on a key it does not own.
     */
    topic: pubsub.TopicArgs;
    /**
     * Every consumer's subscription, alike. A message is redelivered until the
     * run it started settles, so a subscription never dead-letters.
     */
    subscription: pubsub.SubscriptionArgs;
  };
  /**
   * A task: its Pub/Sub topic and its consumer's subscription, patched as a
   * topic's are, and the Cloud Scheduler job that publishes to it on its cron.
   */
  task: {
    topic: pubsub.TopicArgs;
    subscription: pubsub.SubscriptionArgs;
    /**
     * The job that fires a task with a cron; a task with none refuses a patch
     * here. Cloud Scheduler ignores `attemptDeadline` for a Pub/Sub target, so
     * it is not patched.
     */
    job: cloudscheduler.JobArgs;
  };
  /**
   * The Cloud Run service behind each function of a serverless app. Patches
   * to `template.containers` apply to the one container ocel runs, and
   * `template.serviceAccount` names an account your own infrastructure owns,
   * which every service of the app must agree on.
   */
  function: {
    service: cloudrunv2.ServiceArgs;
  };
  /** The Cloud Run service a container app runs as, patched as a function's is. */
  container: {
    service: cloudrunv2.ServiceArgs;
  };
  /**
   * The Cloud Run service behind each worker. Its timeout stays the push
   * deadline ocel keeps, so `template.timeout` is not patched here.
   */
  worker: {
    service: cloudrunv2.ServiceArgs;
  };
  /**
   * The network of the tier, shared by every project deployed to it: the
   * subnetwork Cloud Run reaches kv stores and databases from, and the service
   * connection policies Memorystore and Cloud SQL connect through. A rule sees
   * no `env` or `app` for it, and no binding fills it. Patches from the
   * projects of one tier must agree: a deploy whose patches differ from the
   * ones the network carries makes it again with its own, and a deploy that
   * patches nothing in it leaves it as it is.
   */
  network: {
    /** The tier's VPC network. Its `mtu` is set when ocel creates it, and fixed after. */
    network: compute.NetworkArgs;
    /**
     * The tier's subnetwork. Its `ipCidrRange` is a `/16` to a `/26` inside
     * 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16, 10.240.0.0/20 when left
     * off, and fixed once the subnetwork exists: a deploy that asks for
     * another range is refused.
     */
    subnetwork: compute.SubnetworkArgs;
    /** Both service connection policies, the one for Memorystore and the one for Cloud SQL. */
    connectionPolicy: networkconnectivity.ServiceConnectionPolicyArgs;
  };
}

/** The ocel resource types the GCP provider renders patchable resources for. */
export type GcpResourceType = keyof GcpResourceArgs;

/** Every dotted path to a leaf of `T`, reading through lists of objects. */
export type LeafPaths<T> = T extends readonly (infer E)[]
  ? LeafPaths<E>
  : T extends object
    ? {
        [K in keyof T & string]-?: NonNullable<T[K]> extends readonly (infer E)[]
          ? NonNullable<E> extends object
            ? `${K}.${LeafPaths<NonNullable<E>>}`
            : K
          : NonNullable<T[K]> extends object
            ? `${K}.${LeafPaths<NonNullable<T[K]>>}`
            : K;
      }[keyof T & string]
    : never;

type AllowedPathNames = {
  [T in GcpResourceType]: {
    [K in keyof GcpResourceArgs[T]]: readonly LeafPaths<Unwrap<GcpResourceArgs[T][K]>>[];
  };
};

const topicPaths = [
  "kmsKeyName",
  "messageRetentionDuration",
  "messageStoragePolicy.allowedPersistenceRegions",
  "messageStoragePolicy.enforceInTransit",
] as const;

const subscriptionPaths = ["messageRetentionDuration", "retainAckedMessages"] as const;
const workerServicePaths = [
  "binaryAuthorization.breakglassJustification",
  "binaryAuthorization.policy",
  "binaryAuthorization.useDefault",
  "template.containers.resources.cpuIdle",
  "template.containers.resources.limits.cpu",
  "template.containers.resources.limits.memory",
  "template.containers.resources.limits.nvidia.com/gpu",
  "template.containers.resources.startupCpuBoost",
  "template.encryptionKey",
  "template.executionEnvironment",
  "template.maxInstanceRequestConcurrency",
  "template.nodeSelector.accelerator",
  "template.scaling.maxInstanceCount",
  "template.scaling.minInstanceCount",
  "template.serviceAccount",
  "template.sessionAffinity",
  "template.vpcAccess.egress",
  "template.vpcAccess.networkInterfaces.network",
  "template.vpcAccess.networkInterfaces.subnetwork",
  "template.vpcAccess.networkInterfaces.tags",
] as const;

const appServicePaths = [...workerServicePaths, "template.timeout"] as const;

/**
 * The paths a rule may set under `gcp`, the only ones ocel translates into
 * what it sends Google Cloud. A patch naming any other is refused where it is
 * written, and the deploy refuses it again by name.
 */
export const gcpAllowedPaths = {
  bucket: {
    bucket: [
      "encryption.defaultKmsKeyName",
      "lifecycleRules.action.storageClass",
      "lifecycleRules.action.type",
      "lifecycleRules.condition.age",
      "lifecycleRules.condition.createdBefore",
      "lifecycleRules.condition.customTimeBefore",
      "lifecycleRules.condition.daysSinceCustomTime",
      "lifecycleRules.condition.daysSinceNoncurrentTime",
      "lifecycleRules.condition.matchesPrefixes",
      "lifecycleRules.condition.matchesStorageClasses",
      "lifecycleRules.condition.matchesSuffixes",
      "lifecycleRules.condition.noncurrentTimeBefore",
      "lifecycleRules.condition.numNewerVersions",
      "lifecycleRules.condition.withState",
      "logging.logBucket",
      "logging.logObjectPrefix",
      "retentionPolicy.retentionPeriod",
      "softDeletePolicy.retentionDurationSeconds",
      "storageClass",
      "versioning.enabled",
    ],
  },
  postgres: {
    instance: [
      "settings.availabilityType",
      "settings.backupConfiguration.backupRetentionSettings.retainedBackups",
      "settings.backupConfiguration.backupRetentionSettings.retentionUnit",
      "settings.backupConfiguration.enabled",
      "settings.backupConfiguration.pointInTimeRecoveryEnabled",
      "settings.backupConfiguration.startTime",
      "settings.backupConfiguration.transactionLogRetentionDays",
      "settings.dataCacheConfig.dataCacheEnabled",
      "settings.databaseFlags.name",
      "settings.databaseFlags.value",
      "settings.deletionProtectionEnabled",
      "settings.diskAutoresize",
      "settings.diskAutoresizeLimit",
      "settings.diskSize",
      "settings.diskType",
      "settings.edition",
      "settings.insightsConfig.queryInsightsEnabled",
      "settings.insightsConfig.queryPlansPerMinute",
      "settings.insightsConfig.queryStringLength",
      "settings.insightsConfig.recordApplicationTags",
      "settings.insightsConfig.recordClientAddress",
      "settings.ipConfiguration.pscConfigs.pscAutoConnections.consumerNetwork",
      "settings.maintenanceWindow.day",
      "settings.maintenanceWindow.hour",
      "settings.maintenanceWindow.updateTrack",
      "settings.tier",
    ],
  },
  kv: {
    instance: [
      "automatedBackupConfig.fixedFrequencySchedule.startTime.hours",
      "automatedBackupConfig.retention",
      "deletionProtectionEnabled",
      "desiredAutoCreatedEndpoints.network",
      "maintenancePolicy.weeklyMaintenanceWindows.day",
      "maintenancePolicy.weeklyMaintenanceWindows.startTime.hours",
      "maintenancePolicy.weeklyMaintenanceWindows.startTime.minutes",
      "maintenancePolicy.weeklyMaintenanceWindows.startTime.nanos",
      "maintenancePolicy.weeklyMaintenanceWindows.startTime.seconds",
      "replicaCount",
      "zoneDistributionConfig.mode",
      "zoneDistributionConfig.zone",
    ],
  },
  topic: {
    topic: topicPaths,
    subscription: subscriptionPaths,
  },
  task: {
    topic: topicPaths,
    subscription: subscriptionPaths,
    job: [
      "description",
      "retryConfig.maxBackoffDuration",
      "retryConfig.maxDoublings",
      "retryConfig.maxRetryDuration",
      "retryConfig.minBackoffDuration",
      "retryConfig.retryCount",
    ],
  },
  function: { service: appServicePaths },
  container: { service: appServicePaths },
  worker: { service: workerServicePaths },
  network: {
    network: ["mtu"],
    subnetwork: [
      "ipCidrRange",
      "logConfig.aggregationInterval",
      "logConfig.filterExpr",
      "logConfig.flowSampling",
      "logConfig.metadata",
      "logConfig.metadataFields",
      "privateIpGoogleAccess",
      "secondaryIpRanges.ipCidrRange",
      "secondaryIpRanges.rangeName",
    ],
    connectionPolicy: ["pscConfig.limit"],
  },
} as const satisfies AllowedPathNames;

type GcpAllowed = typeof gcpAllowedPaths;

type AllowedNames<L> = L extends readonly (infer P)[] ? Extract<P, string> : never;

type Head<P extends string> = P extends `${infer H}.${string}` ? H : P;

type Rest<P extends string, K extends string> = P extends `${K}.${infer R}` ? R : never;

type PickPaths<T, P extends string> = T extends readonly (infer E)[]
  ? PickPaths<E, P>[]
  : string extends keyof T
    ? { [K in P]?: T[string] }
    : {
        [K in keyof T & Head<P>]?: K extends P ? T[K] : PickPaths<NonNullable<T[K]>, Rest<P, K>>;
      };

type Translated<T extends GcpResourceType, K extends keyof GcpResourceArgs[T]> = PickPaths<
  Unwrap<GcpResourceArgs[T][K]>,
  AllowedNames<GcpAllowed[T][K & keyof GcpAllowed[T]]>
>;

type WrittenDown<T> = T extends undefined | null
  ? never
  : T extends readonly (infer E)[]
    ? WrittenDown<E>[]
    : T extends object
      ? { readonly [K in keyof T]?: WrittenDown<T[K]> }
      : T;

/**
 * What a rule may patch under `gcp`: the args `@pulumi/gcp` takes for the
 * resource, cut down to the paths ocel translates. A binding output can take
 * the place of any leaf but the network's: it belongs to every project of the
 * tier, so no project's binding fills it.
 */
export type GcpSurfaces = {
  [T in GcpResourceType]: {
    [K in keyof GcpResourceArgs[T]]: T extends "network"
      ? WrittenDown<Translated<T, K>>
      : Patch<Translated<T, K>>;
  };
};
