import { output } from "../shared/args"
import { defineTransform } from "./transform"

export const objectFormStaysValid = defineTransform({
  function: { lambda: { memorySizeMb: 512 } },
})

export const mutateFormSeesDefaultedArgs = defineTransform({
  function: {
    lambda: (args) => {
      args.memorySizeMb = args.memorySizeMb * 2
    },
  },
})

export const returnFormMustBeCompleteArgs = defineTransform({
  function: {
    lambda: (args) => ({ ...args, timeoutSeconds: 60 }),
  },
})

export const partialReturnRejected = defineTransform({
  function: {
    lambda:
      // @ts-expect-error
      () => ({ timeoutSeconds: 60 }),
  },
})

export const contextCarriesResourceIdentity = defineTransform({
  postgres: {
    cluster: (args, ctx) => {
      if (ctx.resourceName === "analytics-db") {
        args.maxCapacity = 8
      }
    },
  },
})

export const gateAndFunctionCompose = defineTransform({
  if: (ctx) => ctx.envClass === "production",
  function: {
    lambda: (args, ctx) => {
      if (ctx.app === "api") {
        args.memorySizeMb = args.memorySizeMb * 2
      }
    },
  },
})

export const linkOutputUsableInsideFunctions = defineTransform({
  function: {
    lambda: (args) => {
      args.vpc = {
        securityGroupIds: [output("network", "appSecurityGroupId")],
        subnetIds: output("network", "privateSubnetIds"),
      }
    },
  },
})
