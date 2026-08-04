// The identity of the isolate serving a request. Module state is per-isolate, so
// the first request an isolate serves mints an id that every later request from
// that same isolate repeats — which is the whole mechanism the probe rests on.
//
// It is minted on first use rather than at module scope because Workers refuses
// crypto in the global scope.

let id: string | undefined;

export function isolateId(): string {
  return (id ??= crypto.randomUUID().slice(0, 8));
}
