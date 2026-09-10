import { writeSync } from "node:fs";
import { isTransformDefinition, type TransformDefinition } from "./define";
import { type EvaluateRequest, evaluate, type TransformModule } from "./evaluate";

/**
 * Pairs a module's authored specifier with its default export, so a deploy
 * that rejects a rule can name the file the author wrote it in.
 */
export function loadModule(specifier: string, exported: unknown): TransformModule {
  if (!isTransformDefinition(exported)) {
    throw new Error(
      `${specifier}: a transform module must export a default \`defineTransform(...)\` result`,
    );
  }
  return { specifier, definition: exported as TransformDefinition };
}

async function readRequest(): Promise<EvaluateRequest> {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk as Buffer);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as EvaluateRequest;
}

/** The descriptor the deploy opens for the answer, leaving stdout to the modules. */
export const resultDescriptor = 3;

/**
 * Runs the deploy-time pass: reads the candidate resources ocel offers on
 * stdin, applies the modules in order, and writes the answer down the
 * descriptor the deploy opened for it. A rejected rule rides down the same
 * descriptor as a refusal, so a module that crashes or fails to import is not
 * read as one, and anything a module prints stays on stdout where it belongs.
 */
export async function runEvaluate(modules: readonly TransformModule[]): Promise<void> {
  try {
    const result = evaluate(await readRequest(), modules);
    writeSync(resultDescriptor, JSON.stringify({ result }));
  } catch (error) {
    const refusal = error instanceof Error ? error.message : String(error);
    writeSync(resultDescriptor, JSON.stringify({ refusal }));
    process.exitCode = 1;
  }
}
