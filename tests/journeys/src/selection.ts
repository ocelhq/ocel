import { offeredBy } from "./offer";
import { coverageFrom, coverVariants, requestedPick } from "./pick";
import {
  type ExampleSpec,
  examplesNamed,
  type Offered,
  specForTarget,
  type Variant,
} from "./spec";
import type { Target } from "./targets/types";

export type Selection = {
  examples: ExampleSpec[];
  naming: Offered;
  covered: Map<string, Variant[]>;
};

export function selectionFor(target: Target, env: NodeJS.ProcessEnv = process.env): Selection {
  const examples = examplesNamed(specForTarget(target.name), env.OCEL_EXAMPLES);
  const offered = offeredBy(target, env);
  return {
    examples,
    naming: target,
    covered: coverVariants(examples, offered, coverageFrom(env), requestedPick(env)),
  };
}
