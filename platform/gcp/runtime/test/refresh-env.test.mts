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

test("a refresh target is read beside the refresh url", () => {
  const target = "https://r0000000a---web-1.a.run.app/_ocel/refresh";

  expect(readRefreshEnv({ ...named, OCEL_REFRESH_TARGET: target }, "s1")?.target).toBe(target);
});

test("no refresh target leaves the target unset", () => {
  expect(readRefreshEnv(named, "s1")?.target).toBeUndefined();
  expect(readRefreshEnv({ ...named, OCEL_REFRESH_TARGET: "" }, "s1")?.target).toBeUndefined();
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

const tasksEmulator = "http://host.docker.internal:7001";
const emulatorKeys = `${tasksEmulator}/oauth2/v3/certs`;

test("a service told no token keys checks refresh tokens against Google's", () => {
  expect(readRefreshEnv(named, "s1")?.certsUrl).toBeUndefined();
});

test("a service on the floci lane checks refresh tokens against the keys its tasks emulator serves", () => {
  const env = {
    ...named,
    OCEL_TASKS_ENDPOINT: tasksEmulator,
    OCEL_ID_TOKEN_CERTS_URL: emulatorKeys,
  };

  expect(readRefreshEnv(env, "s1")?.certsUrl).toBe(emulatorKeys);
});

test.each([
  ["https://evil.example/oauth2/v3/certs", tasksEmulator],
  ["https://www.googleapis.com/oauth2/v3/certs", tasksEmulator],
  [emulatorKeys, undefined],
  ["http://host.docker.internal:7002/oauth2/v3/certs", tasksEmulator],
  ["not a url", tasksEmulator],
])("a service told token keys at %s refuses to start", (certs, tasks) => {
  const env: NodeJS.ProcessEnv = { ...named, OCEL_ID_TOKEN_CERTS_URL: certs };
  if (tasks !== undefined) env.OCEL_TASKS_ENDPOINT = tasks;

  expect(() => readRefreshEnv(env, "s1")).toThrow("OCEL_ID_TOKEN_CERTS_URL");
});
