import { readLive } from "../env/live.js";

const envFragment = (type: string) => {
  const fragment = type.startsWith("ocel:") ? type.slice("ocel:".length) : "";

  if (!fragment) {
    throw new Error(`Unknown resource type '${type}'`);
  }

  return fragment.toUpperCase();
};

export const getConfig = (id: string, type: string) => {
  const key = `OCEL_RESOURCE_${envFragment(type)}_${id}`;
  const value = readLive(key) ?? process.env[key];

  if (!value) {
    throw new Error(
      `Value for ${key} is not defined. Run \`ocel dev\` to resolve it locally, or \`ocel deploy\` to have it delivered from the resource this app links.`,
    );
  }

  return value;
};

export const RUNTIME_ADDRESS = "OCEL_RUNTIME_ADDRESS";

export const getRuntimeAddress = () => {
  const address = process.env[RUNTIME_ADDRESS];

  if (!address) {
    throw new Error(
      `${RUNTIME_ADDRESS} is not defined, so no resource the ocel runtime serves can be reached. Run \`ocel dev\` to serve it locally, or \`ocel deploy\` to have the deployed runtime's address delivered.`,
    );
  }

  return address;
};
