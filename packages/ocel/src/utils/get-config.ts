const envFragment = (type: string) => {
  const fragment = type.startsWith("ocel:") ? type.slice("ocel:".length) : "";

  if (!fragment) {
    throw new Error(`Unknown resource type '${type}'`);
  }

  return fragment.toUpperCase();
};

export const getConfig = (id: string, type: string) => {
  const key = `OCEL_RESOURCE_${envFragment(type)}_${id}`;
  const value = process.env[key];

  if (!value) {
    throw new Error(
      `Value for ${key} is not defined. Are you running Ocel dev ?`,
    );
  }

  return value;
};
