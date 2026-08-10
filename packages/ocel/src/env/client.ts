export type ClientEnv = Readonly<Record<string, string | undefined>>;

export class EnvClientError extends Error {
  override name = "EnvClientError";
}

export const clientEnv: ClientEnv = new Proxy({} as ClientEnv, {
  get(_target, property) {
    if (typeof property === "symbol") return undefined;
    throw new EnvClientError(
      `'${property}' cannot be read: 'ocel/env/client' resolved to its unresolved fallback, because no client accessor is mapped for this app. Run \`ocel dev\` or \`ocel deploy\`: each writes the app's own '.ocel/env-client.ts' and points 'ocel/env/client' at it from the app's tsconfig.json or jsconfig.json, one of which the app must have for the mapping to be stated. Where that config is one ocel cannot write the entry into, the command refuses and names the entry to add by hand.`,
    );
  },
});
