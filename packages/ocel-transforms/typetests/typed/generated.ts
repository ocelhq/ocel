import { bindings, defineTransform } from "@ocel/transforms";

export const placed = defineTransform({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          subnetIds: bindings.custom.network.subnetIds,
          securityGroupIds: bindings.custom.network.securityGroupIds,
        },
      },
    },
  },
});

export const fromCallback = defineTransform(({ bindings: bound }) => ({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          subnetIds: bound.custom.network.subnetIds,
          securityGroupIds: bound.custom.network.securityGroupIds,
        },
      },
    },
  },
}));

export const misspelled = defineTransform({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          // @ts-expect-error the record carries subnetIds, not subnetId
          subnetIds: bindings.custom.network.subnetId,
        },
      },
    },
  },
});

export const wrongProperty = defineTransform({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          // @ts-expect-error the port is a number, and this field takes a list of ids
          subnetIds: bindings.postgres.orders.port,
        },
      },
    },
  },
});

export const unbound = defineTransform({
  aws: {
    function: {
      lambda: {
        vpcConfig: {
          // @ts-expect-error nothing bound a record named cache
          subnetIds: bindings.custom.cache.subnetIds,
        },
      },
    },
  },
});

export const ownedField = defineTransform({
  aws: {
    function: {
      lambda: {
        // @ts-expect-error ocel fills the role from the one it created
        role: "arn:aws:iam::1:role/mine",
      },
    },
  },
});
