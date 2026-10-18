import { errorMessage, openSpan, reportError } from "../protocol.js";

declare global {
  var __ocelRegister: Promise<unknown>[] | undefined;
}

const stage = "discovery";

export async function discover(imports: ReadonlyArray<() => Promise<unknown>>): Promise<void> {
  const end = openSpan(stage);
  let failed = false;
  const fail = (err: unknown) => {
    if (failed) return;
    failed = true;
    reportError(errorMessage(err), undefined, stage);
    end(false);
    process.exitCode = 1;
  };
  process.on("uncaughtException", fail);
  process.on("unhandledRejection", fail);
  try {
    for (const load of imports) await load();
    await Promise.all(globalThis.__ocelRegister ?? []);
    end(true);
  } catch (err) {
    fail(err);
  }
}
