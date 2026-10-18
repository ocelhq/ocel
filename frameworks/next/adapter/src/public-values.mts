import { readPublicKeys } from "./build-env.mjs";

const publicEnvDefine = "process.env.OCEL_PUBLIC_ENV";

function readPublicValues(): Record<string, string> {
  const values: Record<string, string> = {};
  for (const key of readPublicKeys()) {
    const value = process.env[key];
    if (value !== undefined) values[key] = value;
  }
  return values;
}

export function withPublicValues<
  C extends { compiler?: { define?: Record<string, string | number | boolean> } },
>(config: C): C {
  return {
    ...config,
    compiler: {
      ...config.compiler,
      define: {
        ...config.compiler?.define,
        [publicEnvDefine]: JSON.stringify(readPublicValues()),
      },
    },
  };
}
