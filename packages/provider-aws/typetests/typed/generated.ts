import { defineTransform, links } from "@ocel/provider-aws/transform";

export const placed = defineTransform({
  function: {
    vpc: {
      subnetIds: links.network.subnetIds,
      securityGroupIds: links.network.securityGroupIds,
    },
  },
});

export const fromCallback = defineTransform(({ links: published }) => ({
  function: {
    vpc: {
      subnetIds: published.network.subnetIds,
      securityGroupIds: published.network.securityGroupIds,
    },
  },
}));

export const listIntoElement = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the whole list is not one of its elements
      subnetIds: [links.network.subnetIds],
      securityGroupIds: links.network.securityGroupIds,
    },
  },
});

export const misspelled = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the record carries subnetIds, not subnetId
      subnetIds: links.network.subnetId,
      securityGroupIds: links.network.securityGroupIds,
    },
  },
});

export const wrongProperty = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the port is a number, and this field takes a list of ids
      subnetIds: links.orders.port,
      securityGroupIds: links.network.securityGroupIds,
    },
  },
});

export const unpublished = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error nothing published a record named cache
      subnetIds: links.cache.subnetIds,
      securityGroupIds: links.network.securityGroupIds,
    },
  },
});
