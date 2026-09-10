import { bindings, defineTransform } from "ocel/providers/aws/transform";

export const placed = defineTransform({
  function: {
    vpc: {
      subnetIds: bindings.network.subnetIds,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
});

export const fromCallback = defineTransform(({ bindings: published }) => ({
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
      subnetIds: [bindings.network.subnetIds],
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
});

export const misspelled = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the record carries subnetIds, not subnetId
      subnetIds: bindings.network.subnetId,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
});

export const wrongProperty = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the port is a number, and this field takes a list of ids
      subnetIds: bindings.orders.port,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
});

export const unpublished = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error nothing published a record named cache
      subnetIds: bindings.cache.subnetIds,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
});
