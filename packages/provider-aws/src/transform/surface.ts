import type { Linked } from "./output";

/** The environment classes a deploy can target. */
export type EnvClass = "development" | "preview" | "production";

/**
 * What a rule's `if` gate is allowed to decide on: the environment being
 * deployed and the app a candidate resource belongs to. Resources shared
 * across apps carry no `app`, so `ctx.app === "api"` is false for them.
 */
export interface GateContext {
  readonly envClass: EnvClass;
  readonly env: string;
  readonly app: string | undefined;
}

/**
 * What an override function sees: the ambient context plus the name of the
 * resource being rendered. Name-based targeting belongs here, never in a gate.
 */
export interface TransformContext extends GateContext {
  readonly resourceName: string;
}

/**
 * Tags a rule unions into every taggable resource it reaches. Keys under the
 * `ocel:` prefix are ocel's own and are rejected at deploy.
 */
export type TagMap = Record<string, string>;

/** The prefix ocel reserves for the tags it writes itself. */
export const reservedTagPrefix = "ocel:";

/** The tunable args of the Lambda function ocel renders for a route. */
export interface FunctionLambdaSurface {
  memorySizeMb: number;
  timeoutSeconds: number;
  runtime: "nodejs22.x" | "nodejs24.x";
}

/** The tunable args of that function's URL. */
export interface FunctionUrlSurface {
  invokeMode: "RESPONSE_STREAM" | "BUFFERED";
}

/**
 * Where that function's Lambda runs. Both lists empty leaves it outside any
 * VPC, which is how ocel renders it; filling both places it in yours, and the
 * ids are what a link your own infrastructure published carries.
 */
export interface FunctionVpcSurface {
  subnetIds: Linked<Linked<string>[]>;
  securityGroupIds: Linked<Linked<string>[]>;
}

/** The tunable args of the S3 bucket itself. */
export interface BucketBucketSurface {
  forceDestroy: boolean;
}

/** The tunable args of the bucket's CORS configuration. */
export interface BucketCorsSurface {
  allowedOrigins: string[];
  allowedMethods: string[];
  allowedHeaders: string[];
  exposeHeaders: string[];
  maxAgeSeconds: number;
}

/** The tunable args of the Lambda that handles the bucket's object events. */
export interface BucketListenerSurface {
  timeoutSeconds: number;
}

/** The tunable args of the bucket's event notification. */
export interface BucketNotificationSurface {
  events: string[];
}

/** The tunable args of the Aurora Serverless cluster behind a postgres resource. */
export interface PostgresClusterSurface {
  engineVersion: string;
  minCapacity: number;
  maxCapacity: number;
  deletionProtection: boolean;
  skipFinalSnapshot: boolean;
}

/** The tunable args of that cluster's instance. */
export interface PostgresInstanceSurface {
  instanceClass: string;
  publiclyAccessible: boolean;
}

/**
 * Every underlying resource this provider renders that a transform may reach,
 * grouped by the ocel resource that owns it. A key absent here is one the
 * provider does not create, or does not expose as tunable.
 */
export interface AwsSurfaces {
  function: {
    lambda: FunctionLambdaSurface;
    url: FunctionUrlSurface;
    vpc: FunctionVpcSurface;
  };
  bucket: {
    bucket: BucketBucketSurface;
    cors: BucketCorsSurface;
    listener: BucketListenerSurface;
    notification: BucketNotificationSurface;
  };
  postgres: {
    cluster: PostgresClusterSurface;
    instance: PostgresInstanceSurface;
  };
}

/** The ocel resource types that render transformable underlying resources. */
export type SurfaceType = keyof AwsSurfaces;

function allFields<T>() {
  return <F extends readonly (keyof T)[]>(
    ...fields: F &
      ([Exclude<keyof T, F[number]>] extends [never]
        ? unknown
        : { missingFromAllowlist: Exclude<keyof T, F[number]> })
  ): F => fields as unknown as F;
}

/**
 * The allowlist a deploy validates against: every field a transform may set,
 * per underlying resource. Setting anything else fails the deploy by name.
 */
export const surfaceFields = {
  function: {
    lambda: allFields<FunctionLambdaSurface>()(
      "memorySizeMb",
      "timeoutSeconds",
      "runtime",
    ),
    url: allFields<FunctionUrlSurface>()("invokeMode"),
    vpc: allFields<FunctionVpcSurface>()("subnetIds", "securityGroupIds"),
  },
  bucket: {
    bucket: allFields<BucketBucketSurface>()("forceDestroy"),
    cors: allFields<BucketCorsSurface>()(
      "allowedOrigins",
      "allowedMethods",
      "allowedHeaders",
      "exposeHeaders",
      "maxAgeSeconds",
    ),
    listener: allFields<BucketListenerSurface>()("timeoutSeconds"),
    notification: allFields<BucketNotificationSurface>()("events"),
  },
  postgres: {
    cluster: allFields<PostgresClusterSurface>()(
      "engineVersion",
      "minCapacity",
      "maxCapacity",
      "deletionProtection",
      "skipFinalSnapshot",
    ),
    instance: allFields<PostgresInstanceSurface>()(
      "instanceClass",
      "publiclyAccessible",
    ),
  },
} as const satisfies {
  [T in SurfaceType]: {
    [K in keyof AwsSurfaces[T]]: readonly (keyof AwsSurfaces[T][K])[];
  };
};

/** Whether this provider renders transformable resources for the given type. */
export function isSurfaceType(value: string): value is SurfaceType {
  return Object.hasOwn(surfaceFields, value);
}

/**
 * The fields a transform may set on one underlying resource, or `undefined`
 * when the provider renders no such resource for that type.
 */
export function allowedFields(
  type: SurfaceType,
  key: string,
): readonly string[] | undefined {
  const keys = surfaceFields[type] as Record<string, readonly string[]>;
  return Object.hasOwn(keys, key) ? keys[key] : undefined;
}
