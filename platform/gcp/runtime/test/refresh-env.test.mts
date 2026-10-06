import { expect, test } from "vitest";
import { readRefreshEnv } from "../src/next/refresh-env.mjs";

const named = {
  OCEL_REFRESH_URL: "https://web.run.app/_ocel/refresh",
  OCEL_REFRESH_QUEUE: "projects/p/locations/r/queues/q",
  OCEL_REFRESH_ACCOUNT: "refresh@p.iam.gserviceaccount.com",
  OCEL_ISR_PREFIX: "prod/shop/web/r1/isr",
};

test("a service told no refresh url has no refresh settings", () => {
  expect(readRefreshEnv({}, undefined)).toBeUndefined();
});

test("a service told every refresh setting reads them", () => {
  expect(readRefreshEnv(named, "s1")).toEqual({
    url: named.OCEL_REFRESH_URL,
    queue: named.OCEL_REFRESH_QUEUE,
    account: named.OCEL_REFRESH_ACCOUNT,
    isrPrefix: named.OCEL_ISR_PREFIX,
    secret: "s1",
  });
});

test.each(["OCEL_REFRESH_QUEUE", "OCEL_REFRESH_ACCOUNT", "OCEL_ISR_PREFIX"])(
  "a service told a refresh url but not %s refuses to start",
  (name) => {
    const env: NodeJS.ProcessEnv = { ...named };
    delete env[name];

    expect(() => readRefreshEnv(env, "s1")).toThrow(`OCEL_REFRESH_URL is set but ${name} is not`);
  },
);

test("a service told a refresh url but no refresh secret refuses to start", () => {
  expect(() => readRefreshEnv(named, undefined)).toThrow(
    "OCEL_REFRESH_URL is set but OCEL_REFRESH_SECRET is not",
  );
});
