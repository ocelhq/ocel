import { output } from "../shared/args"
import { defineTransform } from "./transform"

export const patchIsShallowPerUnderlyingResource = defineTransform({
  function: { lambda: { memorySizeMb: 512 } },
  postgres: { cluster: { maxCapacity: 4 } },
})

export const linkOutputInStringPosition = defineTransform({
  function: {
    role: { permissionsBoundary: output("org", "permissionsBoundaryArn") },
  },
})

export const linkOutputStandsForAWholeList = defineTransform({
  function: {
    lambda: {
      vpc: {
        securityGroupIds: [output("network", "appSecurityGroupId"), "sg-0abc"],
        subnetIds: output("network", "privateSubnetIds"),
      },
    },
  },
})

export const unknownResourceTypeRejected = defineTransform({
  // @ts-expect-error
  queue: {},
})

export const ticketsAssumedLogGroupKeyRejected = defineTransform({
  function: {
    // @ts-expect-error
    logGroup: { retentionDays: 14 },
  },
})

export const linkOutputWhereNumberExpectedRejected = defineTransform({
  function: {
    lambda: {
      // @ts-expect-error
      memorySizeMb: output("network", "memory"),
    },
  },
})

export const outsideTheAllowlistRejected = defineTransform({
  function: {
    lambda: {
      // @ts-expect-error
      handler: "custom.handler",
    },
  },
})

export const wrongPrimitiveRejected = defineTransform({
  postgres: {
    cluster: {
      // @ts-expect-error
      deletionProtection: "yes",
    },
  },
})

export const gateIsAPredicateOverAmbientContext = defineTransform({
  if: (ctx) => ctx.envClass === "production" && ctx.app === "api",
  function: { lambda: { timeoutSeconds: 60 } },
})

export const gateContextHasNoResourceInfo = defineTransform({
  // @ts-expect-error
  if: (ctx) => ctx.resourceName === "assets",
  function: { lambda: { memorySizeMb: 512 } },
})

export const envClassTypoRejected = defineTransform({
  // @ts-expect-error
  if: (ctx) => ctx.envClass === "prod",
  postgres: { cluster: { deletionProtection: true } },
})

export const gateMustReturnBoolean = defineTransform({
  // @ts-expect-error
  if: (ctx) => ctx.env,
  function: { lambda: { memorySizeMb: 512 } },
})

export const ruleListIsOrdered = defineTransform([
  { function: { lambda: { memorySizeMb: 512 } } },
  { if: (ctx) => ctx.app === "api", function: { lambda: { memorySizeMb: 2048 } } },
])
