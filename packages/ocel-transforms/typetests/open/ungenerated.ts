import { bindings, defineTransform } from "@ocel/transforms";

export const placed = defineTransform({
  aws: {
    function: {
      lambda: {
        memorySize: 1024,
        vpcConfig: {
          subnetIds: bindings.custom.network.privateSubnetIds,
          securityGroupIds: bindings.whatever.anything.atAll,
        },
      },
    },
  },
});

export const perElement = defineTransform(({ bindings: bound }) => ({
  aws: {
    postgres: {
      cluster: { deletionProtection: true },
      instance: { instanceClass: bound.custom.sizing.instanceClass },
    },
  },
}));
