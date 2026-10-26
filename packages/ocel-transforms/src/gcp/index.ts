import type { memorystore, sql, storage } from "@pulumi/gcp";
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
} as const satisfies AllowedPathNames;

type GcpAllowed = typeof gcpAllowedPaths;

type AllowedNames<L> = L extends readonly (infer P)[] ? Extract<P, string> : never;

type Head<P extends string> = P extends `${infer H}.${string}` ? H : P;

type Rest<P extends string, K extends string> = P extends `${K}.${infer R}` ? R : never;

type PickPaths<T, P extends string> = T extends readonly (infer E)[]
  ? PickPaths<E, P>[]
  : {
      [K in keyof T & Head<P>]?: K extends P ? T[K] : PickPaths<NonNullable<T[K]>, Rest<P, K>>;
    };

/**
 * What a rule may patch under `gcp`: the args `@pulumi/gcp` takes for the
 * resource, cut down to the paths ocel translates. A binding output can take
 * the place of any leaf.
 */
export type GcpSurfaces = {
  [T in GcpResourceType]: {
    [K in keyof GcpResourceArgs[T]]: Patch<
      PickPaths<Unwrap<GcpResourceArgs[T][K]>, AllowedNames<GcpAllowed[T][K & keyof GcpAllowed[T]]>>
    >;
  };
};
