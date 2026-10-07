import { describe, expect, it } from "bun:test";
import { findBindingsShown } from "./prerender";

describe("findBindingsShown", () => {
  it("finds a binding assigned in a history line, a database url with its password, and a password field", () => {
    const history = [
      "RUN |1 OCEL_RESOURCE_POSTGRES_my-db={} /bin/sh -c pnpm run build # buildkit",
      "ENV DATABASE_URL=postgres://app:hunter2@127.0.0.1:5432/db",
      '{"Env":["BINDING={\\"password\\": \\"hunter2\\"}"]}',
    ].join("\n");

    expect(findBindingsShown(history)).toEqual([
      "OCEL_RESOURCE_POSTGRES_my-db=",
      "postgres://app:hunter2@",
      '\\"password\\":',
    ]);
  });

  it("finds nothing in a history that mounts the binding as a secret and passes only the live hash", () => {
    const history = [
      "RUN |1 OCEL_LIVE_HASH=9f2c0d /bin/sh -c OCEL_LIVE_DIR=/run/ocel-live pnpm run build # buildkit",
      "ARG OCEL_LIVE_HASH=9f2c0d",
      "WORKDIR /workspace/tests/fixtures/prerender/next-dockerfile",
      "postgres://127.0.0.1:5432/db",
    ].join("\n");

    expect(findBindingsShown(history)).toEqual([]);
  });
});
