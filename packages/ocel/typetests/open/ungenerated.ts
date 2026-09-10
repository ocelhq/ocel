import { bindings, defineTransform } from "ocel/providers/aws/transform";

export const placed = defineTransform({
  function: {
    vpc: {
      subnetIds: bindings.network.privateSubnetIds,
      securityGroupIds: bindings.whatever.anythingAtAll,
    },
  },
});

export const perElement = defineTransform(({ bindings: published }) => ({
  function: {
    vpc: {
      subnetIds: [published.network.subnetA, published.network.subnetB],
      securityGroupIds: published.network.securityGroupIds,
    },
  },
}));
