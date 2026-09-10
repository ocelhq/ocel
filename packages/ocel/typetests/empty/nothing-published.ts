import { bindings, defineTransform } from "ocel/providers/aws/transform";

export const closed = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the coordinate published no record, so no name is open
      subnetIds: bindings.network.subnetIds,
    },
  },
});

export const closedInCallback = defineTransform(({ bindings: published }) => ({
  function: {
    vpc: {
      // @ts-expect-error the coordinate published no record, so no name is open
      subnetIds: published.whatever.anythingAtAll,
    },
  },
}));
