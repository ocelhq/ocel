import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import type { Refusal } from "../matrix/types";
import { fixtureDir } from "../paths";
import { type Check, type CheckContext, json, PASSWORD_REPORT_NONCE_HEADER } from "./context";

const TTL_MS = 2_000;
const EXPIRED_WITHIN_MS = 15_000;
const OVERRIDE_TTL_MS = 60_000;
const CONCURRENT_INCREMENTS = 50;
const PERSISTED_NOTE = "kv persisted key";
const READY_WITHIN_MS = 60_000;

type Sent = { res: Response; body: unknown };

function send(ctx: CheckContext, method: string, at: string, body?: unknown): Promise<Sent> {
  return json(ctx, at, {
    method,
    ...(body === undefined
      ? {}
      : { headers: { "content-type": "application/json" }, body: JSON.stringify(body) }),
  });
}

function newKey(of: string): string {
  return `${of}-${randomUUID()}`;
}

function describeSent(sent: Sent): string {
  return `${sent.res.status} ${JSON.stringify(sent.body)}`;
}

function assertStatus(sent: Sent, status: number, what: string): void {
  assert.equal(sent.res.status, status, `${what} answered ${describeSent(sent)}`);
}

async function waitForStore(ctx: CheckContext): Promise<void> {
  const deadline = Date.now() + READY_WITHIN_MS;
  let last = "nothing";
  while (Date.now() < deadline) {
    const sent = await send(ctx, "GET", "/api/kv/ping").catch((error: unknown) => {
      last = String(error);
      return undefined;
    });
    if (sent?.res.status === 200) {
      return;
    }
    if (sent) {
      last = describeSent(sent);
    }
    await delay(1_000);
  }
  assert.fail(`the store never answered a PING through the app; last: ${last}`);
}

export const kvBindingCheck: Check = {
  title: "GET /api/kv answers with the store it resolved and a password to reach it with",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/api/kv");
    assert.equal(res.status, 200);
    const binding = body as { host: string; port: number; username: string; hasPassword: boolean };
    assert.ok(binding.host.length > 0, "the app resolved no host");
    assert.ok(Number.isInteger(binding.port) && binding.port > 0, "the app resolved no port");
    assert.ok(binding.username.length > 0, "the app resolved no username");
    assert.equal(binding.hasPassword, true);
  },
};

export const kvPingCheck: Check = {
  title: "GET /api/kv/ping answers PONG through the store's client",
  run: async (ctx) => {
    await waitForStore(ctx);
    const { res, body } = await json(ctx, "/api/kv/ping");
    assert.equal(res.status, 200);
    assert.deepEqual(body, { pong: "PONG" });
  },
};

export const kvTextCheck: Check = {
  title: "a text entry reads back the string written under a key",
  run: async (ctx) => {
    const at = `/api/kv/text/${newKey("text")}`;
    const value = `journey-${randomUUID()} ünïcødé`;
    assertStatus(await send(ctx, "PUT", at, { value }), 204, "the write");
    const read = await send(ctx, "GET", at);
    assertStatus(read, 200, "the read");
    assert.deepEqual(read.body, { value });
  },
};

export const kvCounterCheck: Check = {
  title: "a counter entry reads back the integer written, incremented and decremented",
  run: async (ctx) => {
    const at = `/api/kv/counter/${newKey("counter")}`;
    assertStatus(await send(ctx, "PUT", at, { value: 40 }), 204, "the write");
    const up = await send(ctx, "POST", `${at}/increment`, { by: 5 });
    assertStatus(up, 200, "the increment");
    assert.deepEqual(up.body, { value: 45 });
    const down = await send(ctx, "POST", `${at}/decrement`, {});
    assertStatus(down, 200, "the decrement");
    assert.deepEqual(down.body, { value: 44 });
    const read = await send(ctx, "GET", at);
    assertStatus(read, 200, "the read");
    assert.deepEqual(read.body, { value: 44 });
  },
};

export const kvJsonCheck: Check = {
  title: "a json entry reads back the value its schema accepted",
  run: async (ctx) => {
    const at = `/api/kv/json/${newKey("json")}`;
    const value = { name: "ada", visits: 3, tags: ["a", "b/c"] };
    assertStatus(await send(ctx, "PUT", at, { value }), 204, "the write");
    const read = await send(ctx, "GET", at);
    assertStatus(read, 200, "the read");
    assert.deepEqual(read.body, { value });
  },
};

export const kvListCheck: Check = {
  title: "a list entry keeps the order values were pushed and unshifted in",
  run: async (ctx) => {
    const at = `/api/kv/list/${newKey("list")}`;
    const pushed = await send(ctx, "POST", `${at}/push`, { values: ["b", "c"] });
    assertStatus(pushed, 200, "the push");
    assert.deepEqual(pushed.body, { length: 2 });
    const unshifted = await send(ctx, "POST", `${at}/unshift`, { values: ["a"] });
    assertStatus(unshifted, 200, "the unshift");
    assert.deepEqual(unshifted.body, { length: 3 });
    const read = await send(ctx, "GET", at);
    assertStatus(read, 200, "the read");
    assert.deepEqual(read.body, { values: ["a", "b", "c"] });
    const popped = await send(ctx, "POST", `${at}/pop`);
    assertStatus(popped, 200, "the pop");
    assert.deepEqual(popped.body, { value: "c" });
    const shifted = await send(ctx, "POST", `${at}/shift`);
    assertStatus(shifted, 200, "the shift");
    assert.deepEqual(shifted.body, { value: "a" });
  },
};

export const kvSetCheck: Check = {
  title: "a set entry holds each member once and answers whether it holds one",
  run: async (ctx) => {
    const at = `/api/kv/set/${newKey("set")}`;
    const added = await send(ctx, "POST", `${at}/add`, { members: ["x", "y", "x"] });
    assertStatus(added, 200, "the add");
    assert.deepEqual(added.body, { added: 2 });
    const again = await send(ctx, "POST", `${at}/add`, { members: ["y", "z"] });
    assert.deepEqual(again.body, { added: 1 });
    const read = await send(ctx, "GET", at);
    assertStatus(read, 200, "the read");
    assert.deepEqual(read.body, { members: ["x", "y", "z"] });
    assert.deepEqual((await send(ctx, "GET", `${at}/has/y`)).body, { has: true });
    assert.deepEqual((await send(ctx, "GET", `${at}/has/w`)).body, { has: false });
  },
};

export const kvMissCheck: Check = {
  title: "a key never written reads as absent in every shape",
  run: async (ctx) => {
    for (const shape of ["text", "counter", "json"]) {
      const read = await send(ctx, "GET", `/api/kv/${shape}/${newKey("never-written")}`);
      assertStatus(read, 404, `a ${shape} read of a key never written`);
    }
    const list = await send(ctx, "GET", `/api/kv/list/${newKey("never-written")}`);
    assert.deepEqual(list.body, { values: [] });
    const set = await send(ctx, "GET", `/api/kv/set/${newKey("never-written")}`);
    assert.deepEqual(set.body, { members: [] });
  },
};

type Lived = { value: string; ttlMs: number };

async function readLived(ctx: CheckContext, at: string): Promise<Lived | undefined> {
  const read = await send(ctx, "GET", at);
  if (read.res.status === 404) {
    return undefined;
  }
  assertStatus(read, 200, "the read");
  return read.body as Lived;
}

export const kvTtlExpiryCheck: Check = {
  title: "a key written to an entry with a ttl expires after it",
  run: async (ctx) => {
    const at = `/api/kv/ttl/${newKey("expires")}`;
    assertStatus(await send(ctx, "PUT", at, { value: "brief" }), 204, "the write");
    const fresh = await readLived(ctx, at);
    assert.ok(fresh, "the key was absent right after it was written");
    assert.ok(
      fresh.ttlMs > 0 && fresh.ttlMs <= TTL_MS,
      `the key lives ${fresh.ttlMs}ms, not the entry's ${TTL_MS}ms`,
    );
    const deadline = Date.now() + EXPIRED_WITHIN_MS;
    while ((await readLived(ctx, at)) !== undefined) {
      assert.ok(Date.now() < deadline, `the key outlived its ttl by ${EXPIRED_WITHIN_MS}ms`);
      await delay(250);
    }
  },
};

export const kvTtlOverrideCheck: Check = {
  title: "a write that names its own ttl replaces the entry's",
  run: async (ctx) => {
    const at = `/api/kv/ttl/${newKey("override")}`;
    assertStatus(await send(ctx, "PUT", at, { value: "kept", ttl: "60s" }), 204, "the write");
    const lived = await readLived(ctx, at);
    assert.ok(lived, "the key was absent right after it was written");
    assert.ok(
      lived.ttlMs > TTL_MS && lived.ttlMs <= OVERRIDE_TTL_MS,
      `the key lives ${lived.ttlMs}ms, not the ${OVERRIDE_TTL_MS}ms the write named`,
    );
  },
};

export const kvTtlKeepCheck: Check = {
  title: "a write that keeps the ttl leaves the key's current one in place",
  run: async (ctx) => {
    const at = `/api/kv/ttl/${newKey("keep")}`;
    assertStatus(
      await send(ctx, "PUT", at, { value: "first", ttl: "60s" }),
      204,
      "the first write",
    );
    assertStatus(
      await send(ctx, "PUT", at, { value: "second", ttl: "keep" }),
      204,
      "the second write",
    );
    const lived = await readLived(ctx, at);
    assert.ok(lived, "the key was absent right after it was written");
    assert.equal(lived.value, "second");
    assert.ok(
      lived.ttlMs > TTL_MS && lived.ttlMs <= OVERRIDE_TTL_MS,
      `the key lives ${lived.ttlMs}ms, not what remained of the ${OVERRIDE_TTL_MS}ms it had`,
    );
  },
};

export const kvTtlClearCheck: Check = {
  title: "a write that clears the ttl leaves the key to live until it is deleted",
  run: async (ctx) => {
    const at = `/api/kv/ttl/${newKey("clear")}`;
    assertStatus(await send(ctx, "PUT", at, { value: "lasting", ttl: null }), 204, "the write");
    const lived = await readLived(ctx, at);
    assert.ok(lived, "the key was absent right after it was written");
    assert.equal(lived.ttlMs, -1, `the key lives ${lived.ttlMs}ms, not without end`);
    await delay(TTL_MS + 500);
    assert.ok(await readLived(ctx, at), "the key expired with the entry's ttl");
  },
};

export const kvConcurrentIncrementCheck: Check = {
  title: "concurrent increments of one counter each count once",
  run: async (ctx) => {
    const at = `/api/kv/counter/${newKey("concurrent")}`;
    const sums = await Promise.all(
      Array.from({ length: CONCURRENT_INCREMENTS }, async () => {
        const up = await send(ctx, "POST", `${at}/increment`, {});
        assertStatus(up, 200, "an increment");
        return (up.body as { value: number }).value;
      }),
    );
    assert.deepEqual(
      [...sums].sort((a, b) => a - b),
      Array.from({ length: CONCURRENT_INCREMENTS }, (_, i) => i + 1),
    );
    assert.deepEqual((await send(ctx, "GET", at)).body, { value: CONCURRENT_INCREMENTS });
  },
};

export const kvJsonRefusesInvalidWriteCheck: Check = {
  title: "a json entry refuses a value its schema rejects, and writes nothing",
  run: async (ctx) => {
    const at = `/api/kv/json/${newKey("refused")}`;
    const refused = await send(ctx, "PUT", at, { value: { name: 7 } });
    assertStatus(refused, 422, "the invalid write");
    assert.equal((refused.body as { error: string }).error, "InvalidKVValueError");
    assertStatus(await send(ctx, "GET", at), 404, "the read after a refused write");
  },
};

const INVALID_STORED = [
  ["unparseable", "not json at all"],
  ["schema-invalid", '{"name":7}'],
] as const;

export const kvJsonInvalidStoredCheck: Check = {
  title:
    "a json entry throws on a stored value it cannot parse or its schema rejects, or misses where it opts to",
  run: async (ctx) => {
    for (const [invalid, raw] of INVALID_STORED) {
      const id = newKey(invalid);
      for (const entry of ["json", "lenient"]) {
        const written = await send(ctx, "PUT", `/api/kv/raw/${entry}/${id}`, { raw });
        assertStatus(written, 204, `the raw write of a ${invalid} value under the ${entry} entry`);
      }
      const thrown = await send(ctx, "GET", `/api/kv/json/${id}`);
      assertStatus(thrown, 422, `the read of a stored ${invalid} value`);
      assert.equal((thrown.body as { error: string }).error, "InvalidKVValueError");
      assertStatus(
        await send(ctx, "GET", `/api/kv/lenient/${id}`),
        404,
        `the read of a stored ${invalid} value where the entry treats it as a miss`,
      );
    }
  },
};

export const kvParameterSlashCheck: Check = {
  title: "a parameter holding / stays inside its own entry's key",
  run: async (ctx) => {
    const owner = newKey("doc");
    const id = `${owner}/meta`;
    assertStatus(await send(ctx, "PUT", "/api/kv/docs", { id, value: "forged" }), 204, "the write");
    const read = await send(ctx, "POST", "/api/kv/docs/read", { id });
    assertStatus(read, 200, "the read through the same entry");
    assert.deepEqual(read.body, { value: "forged" });
    assertStatus(
      await send(ctx, "GET", `/api/kv/meta/${owner}`),
      404,
      "the read of the entry the slash would have reached",
    );
  },
};

type Filled = { written: number; kept: number; refused?: string };

async function fill(ctx: CheckContext, store: string): Promise<Filled> {
  const filled = await send(ctx, "POST", `/api/kv/fill/${store}`, {});
  assertStatus(filled, 200, `filling ${store}`);
  return filled.body as Filled;
}

export const kvNoEvictionCheck: Check = {
  title: "a store that evicts nothing refuses a write past its memory, and keeps every key",
  run: async (ctx) => {
    const filled = await fill(ctx, "bounded");
    assert.match(filled.refused ?? "", /OOM/, `the store took ${filled.written} values, unrefused`);
    assert.equal(filled.kept, filled.written, "the store dropped a key it was told never to evict");
  },
};

export const kvLruEvictionCheck: Check = {
  title: "a store that evicts by lru takes every write past its memory, evicting keys for them",
  run: async (ctx) => {
    const filled = await fill(ctx, "evicting");
    assert.equal(filled.refused, undefined, `the store refused a write: ${filled.refused}`);
    assert.ok(
      filled.kept < filled.written,
      `the store kept all ${filled.written} values past its memory`,
    );
  },
};

export const kvPersistenceCheck: Check = {
  title: "what a store held before a restart or redeploy is still there after",
  run: async (ctx) => {
    await waitForStore(ctx);
    if (ctx.phase === "verify") {
      const written = newKey("persisted");
      assertStatus(
        await send(ctx, "PUT", `/api/kv/text/${written}`, { value: written }),
        204,
        "the write",
      );
      ctx.notes.set(PERSISTED_NOTE, written);
      return;
    }
    const kept = ctx.notes.get(PERSISTED_NOTE);
    assert.ok(kept, `nothing was written before the ${ctx.phase} to read back`);
    const read = await send(ctx, "GET", `/api/kv/text/${kept}`);
    assertStatus(read, 200, `the read after the ${ctx.phase}`);
    assert.deepEqual(read.body, { value: kept });
  },
};

export const kvPubSubCheck: Check = {
  title: "the native client delivers a message published to a channel a duplicate subscribed to",
  run: async (ctx) => {
    const channel = newKey("channel");
    const sent = await send(ctx, "POST", "/api/kv/native/pubsub", { channel });
    assertStatus(sent, 200, "the round trip");
    assert.deepEqual(sent.body, { received: `hello ${channel}` });
  },
};

export const kvTransactionCheck: Check = {
  title: "the native client runs a transaction's commands together",
  run: async (ctx) => {
    const sent = await send(ctx, "POST", "/api/kv/native/transaction", { key: newKey("multi") });
    assertStatus(sent, 200, "the transaction");
    assert.deepEqual(sent.body, { results: ["OK", 2, "2"] });
  },
};

export const kvBlockingPopCheck: Check = {
  title: "the native client's duplicated connection wakes from a blocking pop on a push",
  run: async (ctx) => {
    const sent = await send(ctx, "POST", "/api/kv/native/blocking-pop", { key: newKey("queue") });
    assertStatus(sent, 200, "the blocking pop");
    assert.deepEqual(sent.body, { popped: "woken" });
  },
};

export const kvUnauthenticatedCheck: Check = {
  title: "a connection to the store without its password is refused",
  run: async (ctx) => {
    const sent = await send(ctx, "GET", "/api/kv/unauthenticated");
    assertStatus(sent, 200, "the attempt");
    const attempt = sent.body as { refused: boolean; error?: string };
    assert.equal(attempt.refused, true, "the store answered a connection that gave no password");
    assert.match(attempt.error ?? "", /NOAUTH|WRONGPASS/);
  },
};

type PasswordReport = { password: string; environment: string[] };

async function readPasswordReport(ctx: CheckContext): Promise<PasswordReport> {
  const sent = await json(ctx, "/api/kv/password-report", {
    headers: { [PASSWORD_REPORT_NONCE_HEADER]: ctx.passwordReportNonce },
  });
  assertStatus(sent, 200, "the password report");
  const shown = sent.body as PasswordReport;
  assert.ok(shown.password.length >= 16, "the app resolved no password worth hiding");
  return shown;
}

export const kvPasswordUnprintedCheck: Check = {
  title: "the store's password shows in clear in nothing ocel prints or runs",
  run: async (ctx) => {
    const { password } = await readPasswordReport(ctx);
    const exposed = await ctx.readExposed();
    assert.ok(exposed.length > 0, "the target exposed nothing to search");
    assert.ok(!exposed.includes(password), "the password shows in clear outside the app");
  },
};

export const kvPasswordOutOfEnvironmentCheck: Check = {
  title: "the store's password is in clear in no environment variable of the app",
  run: async (ctx) => {
    const { environment } = await readPasswordReport(ctx);
    assert.deepEqual(environment, [], `${environment.join(", ")} holds the password in clear`);
  },
};

export const kvChecks: Check[] = [
  kvBindingCheck,
  kvPingCheck,
  kvPersistenceCheck,
  kvTextCheck,
  kvCounterCheck,
  kvJsonCheck,
  kvListCheck,
  kvSetCheck,
  kvMissCheck,
  kvTtlExpiryCheck,
  kvTtlOverrideCheck,
  kvTtlKeepCheck,
  kvTtlClearCheck,
  kvConcurrentIncrementCheck,
  kvJsonRefusesInvalidWriteCheck,
  kvJsonInvalidStoredCheck,
  kvParameterSlashCheck,
  kvNoEvictionCheck,
  kvLruEvictionCheck,
  kvPubSubCheck,
  kvTransactionCheck,
  kvBlockingPopCheck,
  kvUnauthenticatedCheck,
  kvPasswordUnprintedCheck,
  kvPasswordOutOfEnvironmentCheck,
];

export function setsPasswordReportNonce(checks: Check[]): boolean {
  return checks.some(
    (one) => one === kvPasswordUnprintedCheck || one === kvPasswordOutOfEnvironmentCheck,
  );
}

type Site = { file: string; line: number };

function findDeclaringSites(dir: string, marks: string[]): Site[] {
  const sources = readdirSync(dir, { recursive: true, encoding: "utf8" }).filter(
    (file) => /\.(ts|py|go|rs)$/.test(file) && !file.split(path.sep).includes("node_modules"),
  );
  return marks.map((mark) => {
    for (const file of sources) {
      const at = readFileSync(path.join(dir, file), "utf8")
        .split("\n")
        .findIndex((line) => line.includes(mark));
      if (at >= 0) {
        return { file: file.split(path.sep).join("/"), line: at + 1 };
      }
    }
    throw new Error(`no source under ${dir} declares ${mark}`);
  });
}

export function overlapRefusal(fixture: string, patterns: [string, string]): Refusal {
  return {
    title: "the build refuses entries whose patterns overlap, naming both declarations",
    run: async (said) => {
      const sites = findDeclaringSites(
        fixtureDir(fixture),
        patterns.map((one) => `"${one}"`),
      );
      assert.match(said, /overlaps/, `the deploy failed for another reason:\n${said}`);
      for (const { file, line } of sites) {
        assert.ok(
          said.includes(`${file}:${line}`),
          `the refusal does not name ${file}:${line}:\n${said}`,
        );
      }
    },
  };
}
