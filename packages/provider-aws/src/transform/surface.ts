export type EnvClass = "development" | "preview" | "production";

export interface GateContext {
  readonly envClass: EnvClass;
  readonly env: string;
  readonly app: string | undefined;
}

export interface TransformContext extends GateContext {
  readonly resourceName: string;
}

export interface FunctionLambdaSurface {
  memorySizeMb: number;
  timeoutSeconds: number;
  runtime: "nodejs22.x" | "nodejs24.x";
}

export interface FunctionUrlSurface {
  invokeMode: "RESPONSE_STREAM" | "BUFFERED";
}

export interface BucketBucketSurface {
  forceDestroy: boolean;
}

export interface BucketCorsSurface {
  allowedOrigins: string[];
  allowedMethods: string[];
  allowedHeaders: string[];
  exposeHeaders: string[];
  maxAgeSeconds: number;
}

export interface BucketListenerSurface {
  timeoutSeconds: number;
}

export interface BucketNotificationSurface {
  events: string[];
}

export interface PostgresClusterSurface {
  engineVersion: string;
  minCapacity: number;
  maxCapacity: number;
  deletionProtection: boolean;
  skipFinalSnapshot: boolean;
}

export interface PostgresInstanceSurface {
  instanceClass: string;
  publiclyAccessible: boolean;
}

export interface AwsSurfaces {
  function: {
    lambda: FunctionLambdaSurface;
    url: FunctionUrlSurface;
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

export type SurfaceType = keyof AwsSurfaces;

function allFields<T>() {
  return <F extends readonly (keyof T)[]>(
    ...fields: F &
      ([Exclude<keyof T, F[number]>] extends [never]
        ? unknown
        : { missingFromAllowlist: Exclude<keyof T, F[number]> })
  ): F => fields as unknown as F;
}

export const surfaceFields = {
  function: {
    lambda: allFields<FunctionLambdaSurface>()(
      "memorySizeMb",
      "timeoutSeconds",
      "runtime",
    ),
    url: allFields<FunctionUrlSurface>()("invokeMode"),
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

export function isSurfaceType(value: string): value is SurfaceType {
  return Object.hasOwn(surfaceFields, value);
}

export function allowedFields(
  type: SurfaceType,
  key: string,
): readonly string[] | undefined {
  const keys = surfaceFields[type] as Record<string, readonly string[]>;
  return Object.hasOwn(keys, key) ? keys[key] : undefined;
}
