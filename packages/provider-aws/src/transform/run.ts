import type { TransformRule } from "./define";
import {
  evaluate,
  type EvaluateRequest,
  type TransformModule,
} from "./evaluate";

/**
 * Pairs a module's authored specifier with its default export, so a deploy
 * that rejects a rule can name the file the author wrote it in.
 */
export function loadModule(
  specifier: string,
  exported: unknown,
): TransformModule {
  if (!Array.isArray(exported)) {
    throw new Error(
      `${specifier}: a transform module must export a default \`defineTransform(...)\` result`,
    );
  }
  return { specifier, rules: exported as readonly TransformRule[] };
}

async function readRequest(): Promise<EvaluateRequest> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk as Buffer);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as EvaluateRequest;
}

/**
 * Runs the deploy-time pass: reads the candidate resources ocel offers on
 * stdin, applies the modules in order, and writes the result to stdout. A
 * rejected rule leaves its reason on stderr and fails the deploy.
 */
export async function runEvaluate(
  modules: readonly TransformModule[],
): Promise<void> {
  try {
    process.stdout.write(JSON.stringify(evaluate(await readRequest(), modules)));
  } catch (error) {
    process.stderr.write(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
