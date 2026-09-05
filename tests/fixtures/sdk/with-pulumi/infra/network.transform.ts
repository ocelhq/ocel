import { defineTransform } from "@ocel/provider-aws/transform";

export default defineTransform(({ links }) => ({
  function: {
    vpc: {
      subnetIds: links.network.subnetIds,
      securityGroupIds: links.network.securityGroupIds,
    },
  },
}));
