export declare const linkOutputBrand: unique symbol

export interface LinkOutput {
  readonly [linkOutputBrand]: true
  readonly link: string
  readonly property: string
}

export declare function output(link: string, property: string): LinkOutput

export type Input = string | LinkOutput
export type InputList = readonly Input[] | LinkOutput

export type TagMap = Record<string, string>

export interface FunctionRoleSurface {
  permissionsBoundary: Input | undefined
  tags: TagMap
}

export interface FunctionLambdaSurface {
  memorySizeMb: number
  timeoutSeconds: number
  runtime: "nodejs22.x" | "nodejs24.x"
  vpc: { securityGroupIds: InputList; subnetIds: InputList } | undefined
  tags: TagMap
}

export interface FunctionUrlSurface {
  invokeMode: "RESPONSE_STREAM" | "BUFFERED"
}

export interface BucketBucketSurface {
  forceDestroy: boolean
  tags: TagMap
}

export interface BucketCorsSurface {
  allowedOrigins: string[]
  allowedMethods: string[]
  maxAgeSeconds: number
}

export interface BucketListenerSurface {
  memorySizeMb: number
  timeoutSeconds: number
  tags: TagMap
}

export interface BucketNotificationSurface {
  events: string[]
}

export interface PostgresSecurityGroupSurface {
  ingressCidrs: Input[]
  tags: TagMap
}

export interface PostgresSubnetGroupSurface {
  subnetIds: InputList
  tags: TagMap
}

export interface PostgresClusterSurface {
  engineVersion: string
  minCapacity: number
  maxCapacity: number
  deletionProtection: boolean
  skipFinalSnapshot: boolean
  tags: TagMap
}

export interface PostgresInstanceSurface {
  instanceClass: string
  publiclyAccessible: boolean
  tags: TagMap
}

export interface AwsSurfaces {
  function: {
    role: FunctionRoleSurface
    lambda: FunctionLambdaSurface
    url: FunctionUrlSurface
  }
  bucket: {
    bucket: BucketBucketSurface
    cors: BucketCorsSurface
    listener: BucketListenerSurface
    notification: BucketNotificationSurface
  }
  postgres: {
    securityGroup: PostgresSecurityGroupSurface
    subnetGroup: PostgresSubnetGroupSurface
    cluster: PostgresClusterSurface
    instance: PostgresInstanceSurface
  }
}

export type Patch<T> = { readonly [K in keyof T]?: T[K] }
