import { defineTransform, links } from "@ocel/provider-aws/transform";

export const closed = defineTransform({
  function: {
    vpc: {
      // @ts-expect-error the coordinate published no record, so no name is open
      subnetIds: links.network.subnetIds,
    },
  },
});

export const closedInCallback = defineTransform(({ links: published }) => ({
  function: {
    vpc: {
      // @ts-expect-error the coordinate published no record, so no name is open
      subnetIds: published.whatever.anythingAtAll,
    },
  },
}));
