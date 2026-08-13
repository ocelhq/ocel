export const PROTOCOL_PREFIX = "@@OCEL_V1@@";

type Level = "debug" | "info" | "warn" | "error";

interface ProtocolRecord {
  type: "log" | "span_start" | "span_end" | "error";
  app?: string;
  stage?: string;
  level?: Level;
  message?: string;
  id?: string;
  ok?: boolean;
}

function emit(record: ProtocolRecord): void {
  process.stdout.write("\n" + PROTOCOL_PREFIX + JSON.stringify(record) + "\n");
}

export function log(level: Level, message: string, app?: string, stage?: string): void {
  emit({ type: "log", level, message, app, stage });
}

export function reportError(message: string, app?: string, stage?: string): void {
  emit({ type: "error", message, app, stage });
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? (err.stack ?? err.message) : String(err);
}

const reported = new WeakSet<object>();

export function isReported(err: unknown): boolean {
  return typeof err === "object" && err !== null && reported.has(err);
}

let spanCounter = 0;

export async function withSpan<T>(stage: string, app: string | undefined, fn: () => Promise<T>): Promise<T> {
  const id = `${stage}:${app ?? ""}:${++spanCounter}`;
  emit({ type: "span_start", id, stage, app });
  try {
    const result = await fn();
    emit({ type: "span_end", id, ok: true });
    return result;
  } catch (err) {
    reportError(errorMessage(err), app, stage);
    emit({ type: "span_end", id, ok: false });
    if (typeof err === "object" && err !== null) reported.add(err);
    throw err;
  }
}
