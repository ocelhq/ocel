export interface KeyValueStore {
  get<T>(key: string): Promise<T | undefined>;
  put(key: string, value: string): Promise<void>;
}

const BUILD_KEY = "isrPrefix";

export async function claimBuild(store: KeyValueStore, isrPrefix: string): Promise<void> {
  await store.put(BUILD_KEY, isrPrefix);
}

export function claimedBuild(store: KeyValueStore): Promise<string | undefined> {
  return store.get<string>(BUILD_KEY);
}
