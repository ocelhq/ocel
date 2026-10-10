import { defineEnv } from "ocel/env/next";
import { z } from "zod";
import type { env } from "./env";

type Equal<A, B> =
  (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
const expect = <T extends true>() => undefined as unknown as T;

expect<Equal<typeof env.NEXT_PUBLIC_API_URL, string>>();
expect<Equal<typeof env.NEXT_PUBLIC_RETRIES, number>>();
expect<Equal<typeof env.STRIPE_KEY, string>>();

// @ts-expect-error a NEXT_PUBLIC_ key may not be sensitive
defineEnv({ NEXT_PUBLIC_LEAK: { class: "sensitive" } });

// @ts-expect-error a NEXT_PUBLIC_ key may not be secret
defineEnv({ NEXT_PUBLIC_LEAK: { class: "secret", schema: z.string() } });

defineEnv({ NEXT_PUBLIC_FINE: { class: "plain" }, PRIVATE: { class: "secret" } });
