import { defineTransform, links } from "@ocel/provider-aws/transform";

export const placed = defineTransform({
  function: {
    vpc: {
      subnetIds: links.network.privateSubnetIds,
      securityGroupIds: links.whatever.anythingAtAll,
    },
  },
});

export const perElement = defineTransform(({ links: published }) => ({
  function: {
    vpc: {
      subnetIds: [published.network.subnetA, published.network.subnetB],
      securityGroupIds: published.network.securityGroupIds,
    },
  },
}));
