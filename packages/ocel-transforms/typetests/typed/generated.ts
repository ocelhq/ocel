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

export const ownedRuntime = defineTransform({
  aws: {
    function: {
      lambda: {
        // @ts-expect-error the runtime pairs with the artifact ocel built
        runtime: "python3.13",
      },
    },
  },
});

export const ownedCodeLocation = defineTransform({
  aws: {
    function: {
      lambda: {
        // @ts-expect-error the object version pins the artifact ocel uploaded
        s3ObjectVersion: "an-older-object",
      },
    },
  },
});

export const ownedCompleterRuntime = defineTransform({
  aws: {
    bucket: {
      uploadCompleter: {
        // @ts-expect-error ocel places the upload completer's own code
        packageType: "Image",
      },
    },
  },
});

export const attachedPolicy = defineTransform({
  aws: {
    function: {
      role: { path: "/ocel/", maxSessionDuration: 7200 },
      urlPermission: { statementId: "open-to-the-world" },
    },
  },
});

export const ownedRolePolicies = defineTransform({
  aws: {
    function: {
      role: {
        // @ts-expect-error ocel attaches the role's policies as resources of their own
        inlinePolicies: [],
      },
    },
  },
});
