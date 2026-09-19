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
    vps: { postgres: { container: { args: ["-c", "max_connections=200"], shmSize: "256m" } } },
  },
  {
    tags: { "acme:cost-center": "platform" },
  },
]);
