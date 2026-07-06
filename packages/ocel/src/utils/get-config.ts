import { ResourceType } from "./rpc";

const p = (type: string) => (id: string) =>
  `OCEL_RESOURCE_${type.toUpperCase()}_${id}`;

const resourceToString = (t: ResourceType) => {
  switch (t) {
    case ResourceType.POSTGRES: {
      return "POSTGRES";
    }
    default:
      throw new Error("Unknown resource type");
  }
};

export const getConfig = <T extends ResourceType>(id: string, type: T) => {
  const key = p(resourceToString(type))(id);
  const value = process.env[key];

  if (!value) {
    throw new Error(
      `Value for ${key} is not defined. Are you running Ocel dev ?`,
    );
  }

  return value;
};
