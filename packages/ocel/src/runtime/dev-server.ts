/** The address of the running `ocel dev` server; undefined outside it. */
export const OCEL_DEV_SERVER = process.env.OCEL_DEV_SERVER;

/** The env key the token every call to the dev server presents is delivered under. */
export const DEV_SERVER_TOKEN = "OCEL_DEV_SERVER_TOKEN";

/** The delivered dev-server token; empty when none was delivered. */
export const getDevServerToken = () => process.env[DEV_SERVER_TOKEN] ?? "";

/** The header every call to the dev server names this SDK's version in. */
export const SDK_VERSION_HEADER = "Ocel-Sdk-Version";
