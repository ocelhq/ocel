export const OCEL_DEV_SERVER = process.env.OCEL_DEV_SERVER;

export const DEV_SERVER_TOKEN = "OCEL_DEV_SERVER_TOKEN";

export const getDevServerToken = () => process.env[DEV_SERVER_TOKEN] ?? "";
