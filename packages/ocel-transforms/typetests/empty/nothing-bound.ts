import { bindings, defineTransform } from "@ocel/transforms";

export const closed = defineTransform({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          // @ts-expect-error the coordinate bound no record, so no name is open
          subnetIds: bindings.custom.network.subnetIds,
        },
      },
    },
  },
});

export const closedInCallback = defineTransform(({ bindings: bound }) => ({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          // @ts-expect-error the coordinate bound no record, so no name is open
          subnetIds: bound.custom.network.subnetIds,
        },
      },
    },
  },
}));
