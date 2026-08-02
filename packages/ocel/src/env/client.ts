// The browser half of `ocel/env`. Application code imports `clientEnv` from
// `ocel/env/client`, and inside an app build that specifier resolves to the
// accessor generated for that app at `<app>/.ocel/env-client.ts`, not to this
// file.
//
// It is generated, and generated into the app, because of what the
// framework's static replacement can see. Replacement rewrites a literal
// `process.env.NEXT_PUBLIC_KEY` member expression and nothing else, so the
// accessor has to name each value literally — a proxy, a lookup by variable, or
// any indirection a package could ship is opaque to it and would survive into
// the browser as a read of an environment that is not there. Keeping the
// mechanism the framework's own is the point: no custom build-step transform to
// track across framework versions.
//
// This module is what a build that never generated the accessor lands on.

export type ClientEnv = Readonly<Record<string, string | undefined>>;

// EnvClientError is a client-accessible value read where no accessor was
// generated. It is its own error because nothing is wrong with the value or the
// declaration: the project was built without the step that writes the accessor.
export class EnvClientError extends Error {
  override name = "EnvClientError";
}

export const clientEnv: ClientEnv = new Proxy({} as ClientEnv, {
  get(_target, property) {
    // Symbols are how the runtime inspects an object; they are never a
    // variable, and answering them keeps the object printable.
    if (typeof property === "symbol") return undefined;
    throw new EnvClientError(
      `'${property}' cannot be read: 'ocel/env/client' resolved to its unresolved fallback, because no client accessor is mapped for this app. Run \`ocel dev\` or \`ocel deploy\`: each writes the app's own '.ocel/env-client.ts' and points 'ocel/env/client' at it from the app's tsconfig.json or jsconfig.json, one of which the app must have for the mapping to be stated. Where that config is one ocel cannot write the entry into, the command refuses and names the entry to add by hand.`,
    );
  },
});
