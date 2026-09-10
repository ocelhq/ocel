import { defineTransform } from "ocel/providers/aws/transform";

export default defineTransform(({ bindings }) => ({
  function: {
    vpc: {
      subnetIds: bindings.network.subnetIds,
      securityGroupIds: bindings.network.securityGroupIds,
    },
  },
}));
