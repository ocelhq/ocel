import { errorMessage, openSpan, reportError } from "../protocol.js";
import { importModules, type ModuleImport } from "../tracing/spans.js";
import { flushTracing, inSpan, startTracing } from "../tracing/tracing.js";

declare global {
  var __ocelRegister: Promise<unknown>[] | undefined;
}

const stage = "discovery";

export async function discover(imports: ReadonlyArray<ModuleImport>): Promise<void> {
  await startTracing("ocel-discovery");
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
    await inSpan("discover resources", { "ocel.import_count": imports.length }, async () => {
      await importModules(imports, inSpan);
      const registrations = globalThis.__ocelRegister ?? [];
      await inSpan("register resources", { "ocel.registration_count": registrations.length }, () =>
        Promise.all(registrations),
      );
    });
    end(true);
  } catch (err) {
    fail(err);
  }
  await flushTracing();
}
