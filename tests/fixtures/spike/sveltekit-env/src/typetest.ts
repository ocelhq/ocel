import { defineEnvVars } from "ocel/env/sveltekit";
import type { BUILD_TAG, PORT_HINT, STRIPE_KEY } from "$app/env/private";
import type { PUBLIC_API_URL, PUBLIC_RETRIES } from "$app/env/public";

type Equal<A, B> =
  (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
const expect = <T extends true>(_value?: unknown) => undefined as unknown as T;

expect<Equal<typeof STRIPE_KEY, string>>();
expect<Equal<typeof BUILD_TAG, string>>();
expect<Equal<typeof PORT_HINT, number>>();
expect<Equal<typeof PUBLIC_API_URL, string>>();
expect<Equal<typeof PUBLIC_RETRIES, number>>();

// @ts-expect-error a public variable may not be secret
defineEnvVars({ PUBLIC_LEAK: { class: "secret", public: true } });

// @ts-expect-error a public variable may not be sensitive
defineEnvVars({ PUBLIC_LEAK: { class: "sensitive", public: true } });

defineEnvVars({ PUBLIC_FINE: { class: "plain", public: true }, HIDDEN: { class: "secret" } });
