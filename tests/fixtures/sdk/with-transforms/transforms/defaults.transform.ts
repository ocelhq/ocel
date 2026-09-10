import { defineTransform } from "@ocel/transforms";

export default defineTransform([
  {
    aws: { function: { lambda: { memorySize: 2048, timeout: 60 } } },
  },
  {
    if: ({ envClass }) => envClass === "production",
    aws: {
      postgres: {
        cluster: { serverlessv2ScalingConfiguration: { minCapacity: 2, maxCapacity: 16 } },
      },
    },
  },
  {
    tags: { "acme:cost-center": "platform" },
  },
]);
