// Insertion-order LRU over a plain Map: re-inserting a key moves it to the
// newest slot, so the oldest key is always at the front to evict once the
// bound is exceeded. Shared by every per-isolate memo in this worker
// (interception's entry memo, the deployments record cache).
export function lruSet<K, V>(map: Map<K, V>, key: K, value: V, max: number): void {
  map.delete(key);
  map.set(key, value);
  if (map.size > max) {
    const oldest = map.keys().next().value;
    if (oldest !== undefined) map.delete(oldest);
  }
}
