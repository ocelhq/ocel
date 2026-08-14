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
