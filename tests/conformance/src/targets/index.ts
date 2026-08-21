import { createAwsTarget } from "./aws";
import { createDevTarget } from "./dev";
import type { Target } from "../types";

export function createTarget(name: string, token: string): Target {
  if (name === "dev") return createDevTarget(token);
  if (name === "aws") return createAwsTarget(token);
  throw new Error(`unknown conformance target ${JSON.stringify(name)}`);
}
