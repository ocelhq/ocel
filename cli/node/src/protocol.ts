import { Refusal } from "@framework/node-build/refusal";

export const PROTOCOL_PREFIX = "@@OCEL_V1@@";

type Level = "debug" | "info" | "warn" | "error";

interface ProtocolRecord {
  type: "log" | "output" | "span_start" | "span_end" | "error";
  app?: string;
  stage?: string;
  level?: Level;
  message?: string;
  id?: string;
  ok?: boolean;
  partial?: boolean;
}

function emit(record: ProtocolRecord): void {
  process.stdout.write(`\n${PROTOCOL_PREFIX}${JSON.stringify(record)}\n`);
}

export function log(level: Level, message: string, app?: string, stage?: string): void {
  emit({ type: "log", level, message, app, stage });
}

export function printOutput(app: string, text: string, partial?: boolean): void {
  emit({ type: "output", app, message: text, partial });
}

export function reportError(message: string, app?: string, stage?: string): void {
  emit({ type: "error", message, app, stage });
}

export function errorMessage(err: unknown): string {
  if (err instanceof Refusal) return err.message;
  return err instanceof Error ? (err.stack ?? err.message) : String(err);
}

const reported = new WeakSet<object>();

export function isReported(err: unknown): boolean {
  return typeof err === "object" && err !== null && reported.has(err);
}

export function reportFailure(err: unknown): void {
  if (!isReported(err)) reportError(errorMessage(err));
}

let spanCounter = 0;

export function openSpan(stage: string, app?: string): (ok: boolean) => void {
  const id = `${stage}:${app ?? ""}:${++spanCounter}`;
  emit({ type: "span_start", id, stage, app });
  let ended = false;
  return (ok) => {
    if (ended) return;
    ended = true;
    emit({ type: "span_end", id, ok });
  };
}

export async function withSpan<T>(
  stage: string,
  app: string | undefined,
  fn: () => Promise<T>,
): Promise<T> {
  const end = openSpan(stage, app);
  try {
    const result = await fn();
    end(true);
    return result;
  } catch (err) {
    reportError(errorMessage(err), app, stage);
    end(false);
    if (typeof err === "object" && err !== null) reported.add(err);
    throw err;
  }
}
