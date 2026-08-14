import { output } from "../shared/args"
import { defineTransform } from "./transform"

export default defineTransform([
  {
    tags: {
      "acme:cost-center": "platform",
      "acme:env": "production",
    },
  },
  {
    function: {
      lambda: (args) => {
        args.memorySizeMb = 512
        args.vpc = {
          securityGroupIds: [output("network", "appSecurityGroupId")],
          subnetIds: output("network", "privateSubnetIds"),
        }
      },
    },
  },
])
