// The snapshot Durable Object's storage-class logic: the one thing that object
// must remember. Kept separate from the DO class (isr-snapshot.ts) and the HTTP
// surface (index.ts) so it can be exercised directly against a real instance's
// storage, the same way registry.ts is.
//
// It exists because a heartbeat alarm is handed nothing at all and a running
// object's ctx.id no longer carries the name it was addressed by. Without this,
// an object evicted between beats would wake up not knowing which build's clock
// it publishes.

// The subset of DurableObjectStorage this module calls. A real ctx.storage
// satisfies it structurally.
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
