/** One `provider.variablestore.v1.PublishedValue`, ready for protobuf JSON. */
export interface PublishedValue {
  key: string;
  value: string;
  secret?: boolean;
}

/** One `provider.variablestore.v1.PublishedValues`, ready for protobuf JSON: every value one call publishes. */
export interface PublishedValues {
  values: PublishedValue[];
}

export function publishedValues(
  values: Record<string, unknown>,
  secrets: string[],
): PublishedValues {
  const keys = Object.keys(values).sort();
  if (keys.length === 0) {
    throw new Error(
      "bind.variables publishes no values; a call is the values an app reads, so remove one that names none",
    );
  }
  return {
    values: keys.map((key) => {
      if (!key) {
        throw new Error(
          "bind.variables publishes a value under no name; the name is the variable an app declares",
        );
      }
      const value = values[key];
      if (value === undefined || value === null) {
        throw new Error(
          `${key} has no value; every variable bind.variables publishes needs one the update can write`,
        );
      }
      const published: PublishedValue = {
        key,
        value: typeof value === "string" ? value : JSON.stringify(value),
      };
      if (secrets.includes(key)) {
        published.secret = true;
      }
      return published;
    }),
  };
}

/** One `provider.variablestore.v1.PublishedVersion`, ready for protobuf JSON: a key and the version of it one publish wrote. */
export interface PublishedVersion {
  key: string;
  version: string;
}

/** One `provider.variablestore.v1.PublishedVersions`, ready for protobuf JSON: what one call wrote, to remove at those versions. */
export interface PublishedVersions {
  values: PublishedVersion[];
}

interface PublishResult {
  written?: { coordinate?: { key?: string }; version?: string }[];
}

export function writtenVersions(
  published: PublishedValues,
  result: unknown,
): Record<string, string> {
  const reported: Record<string, string> = {};
  for (const written of (result as PublishResult | undefined)?.written ?? []) {
    const key = written.coordinate?.key;
    if (key && written.version) {
      reported[key] = written.version;
    }
  }
  const versions: Record<string, string> = {};
  for (const { key } of published.values) {
    const version = reported[key];
    if (!version) {
      throw new Error(
        `ocel env publish reported no version for ${key}, and removing this call removes only the versions it wrote; run an ocel CLI that reports them`,
      );
    }
    versions[key] = version;
  }
  return versions;
}

export function publishedVersions(versions: Record<string, string>): PublishedVersions {
  return {
    values: Object.keys(versions)
      .sort()
      .map((key) => ({ key, version: versions[key] as string })),
  };
}
