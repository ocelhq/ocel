import { defineTransform } from "@ocel/provider-aws/transform";

export default defineTransform([
  {
    function: { lambda: { memorySizeMb: 2048, timeoutSeconds: 60 } },
  },
  {
    if: ({ envClass }) => envClass === "production",
    postgres: {
      cluster: (args) => ({
        ...args,
        minCapacity: 2,
        maxCapacity: 16,
        deletionProtection: true,
      }),
    },
  },
  {
    tags: { "acme:cost-center": "platform" },
  },
]);
