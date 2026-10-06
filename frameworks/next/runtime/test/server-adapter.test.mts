import { join } from "node:path";
import { expect, test } from "vitest";
import { newServerAdapter } from "../src/server-adapter.mjs";

const server = { phase: "phase-production-server" };

test("points Next's production server at the cache handlers in its own directory", () => {
  const adapter = newServerAdapter("/ocel/next", () => {});

  const config = adapter.modifyConfig({ output: "standalone" }, server);

  expect(config).toEqual({
    output: "standalone",
    cacheMaxMemorySize: 0,
    cacheHandler: join("/ocel/next", "cache-handler.cjs"),
    cacheHandlers: {
      default: join("/ocel/next", "use-cache-default.cjs"),
      remote: join("/ocel/next", "use-cache-remote.cjs"),
    },
  });
});

test("leaves a build's config exactly as the build configured it", () => {
  let installs = 0;
  const adapter = newServerAdapter("/ocel/next", () => installs++);
  const config = { cacheHandler: "mine.js" };

  expect(adapter.modifyConfig(config, { phase: "phase-production-build" })).toBe(config);
  expect(installs).toBe(0);
});

test("installs its host once however often Next loads the config", () => {
  let installs = 0;
  const adapter = newServerAdapter("/ocel/next", () => installs++);

  adapter.modifyConfig({}, server);
  adapter.modifyConfig({}, server);
  adapter.modifyConfig({}, server);

  expect(installs).toBe(1);
});

test("keeps a use-cache handler the app registered under another name", () => {
  const adapter = newServerAdapter("/ocel/next", () => {});

  const config = adapter.modifyConfig({ cacheHandlers: { sessions: "/app/sessions.js" } }, server);

  expect(config.cacheHandlers).toEqual({
    sessions: "/app/sessions.js",
    default: join("/ocel/next", "use-cache-default.cjs"),
    remote: join("/ocel/next", "use-cache-remote.cjs"),
  });
});
