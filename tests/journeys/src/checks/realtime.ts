import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import { createRealtimeClient, type RealtimeError } from "ocel/realtime/client";
import {
  type Check,
  type CheckContext,
  describeResponse,
  JOURNEY_NONCE_HEADER,
  json,
} from "./context";
import { EventSocket, SocketRefusal } from "./eventSocket";
import {
  type DecodedToken,
  decodeToken,
  type Forgery,
  forgeRefusedCases,
  readTokenVectors,
  type Signer,
} from "./tokenVectors";

const HANDLER_PATH = "/api/realtime";
const SILENCE_MS = 1_000;
const SETTLED_WITHIN_MS = 15_000;
const MAX_OPERATIONS = 50;
const MAX_SUBSCRIPTIONS = 200;
const MAX_EVENT_BYTES = 240 * 1024;
const FOREIGN_ORIGIN = "https://elsewhere.example";

type Operation = {
  op: "subscribe" | "publish";
  pattern: string;
  params?: Record<string, string>;
  body?: unknown;
};

type Batch = { connect?: boolean; ops: Operation[] };

type HandlerAnswer = {
  transport: string;
  url: string;
  host?: string;
  connect?: { token: string; expiresAt: number };
  grants: { i: number; wire: string; token?: string }[];
  denied: { i: number; code: string }[];
};

type Caller = { user: string; orders?: string[]; projects?: string[]; publish?: boolean };

type FixtureChannels = { "orders/:orderId": { event: { status: string } } };

function encodeBearer(caller: Caller): string {
  const claims = new URLSearchParams({ user: caller.user });
  if (caller.orders) claims.set("orders", caller.orders.join(","));
  if (caller.projects) claims.set("projects", caller.projects.join(","));
  if (caller.publish) claims.set("publish", "yes");
  return `Bearer ${claims}`;
}

function newId(of: string): string {
  return `${of}-${randomUUID().slice(0, 8)}`;
}

function postBatch(ctx: CheckContext, batch: unknown, headers: Record<string, string> = {}) {
  return json(ctx, HANDLER_PATH, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body: JSON.stringify(batch),
  });
}

async function requestBatch(
  ctx: CheckContext,
  batch: Batch,
  caller?: Caller,
): Promise<HandlerAnswer> {
  const sent = await postBatch(ctx, batch, caller ? { authorization: encodeBearer(caller) } : {});
  assert.equal(sent.res.status, 200, `the realtime handler answered ${sent.text}`);
  return sent.body as HandlerAnswer;
}

function buildSubscribe(pattern: string, params: Record<string, string> = {}): Operation {
  return { op: "subscribe", pattern, params };
}

function buildPublish(pattern: string, params: Record<string, string>, body: unknown): Operation {
  return { op: "publish", pattern, params, body };
}

async function publishFromServer(
  ctx: CheckContext,
  pattern: string,
  params: Record<string, string>,
  body: unknown,
): Promise<{ status: number; code?: string; error?: string }> {
  const sent = await json(ctx, "/api/publish", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ pattern, params, body }),
  });
  return { status: sent.res.status, ...(sent.body as { code?: string; error?: string }) };
}

async function assertPublishedFromServer(
  ctx: CheckContext,
  pattern: string,
  params: Record<string, string>,
  body: unknown,
): Promise<void> {
  const published = await publishFromServer(ctx, pattern, params, body);
  assert.equal(published.status, 204, `the server publish answered ${JSON.stringify(published)}`);
}

function readGrant(answer: HandlerAnswer, i: number): { wire: string; token: string } {
  const grant = answer.grants.find((one) => one.i === i);
  assert.ok(grant?.token, `op ${i} was not granted a token: ${JSON.stringify(answer)}`);
  return { wire: grant.wire, token: grant.token };
}

function readConnectToken(answer: HandlerAnswer): string {
  assert.ok(answer.connect, `the handler minted no connect token: ${JSON.stringify(answer)}`);
  return answer.connect.token;
}

function openEventSocket(answer: HandlerAnswer): Promise<EventSocket> {
  return EventSocket.open(answer.url, answer.host, readConnectToken(answer));
}

async function runWithSocket(answer: HandlerAnswer, use: (socket: EventSocket) => Promise<void>) {
  const socket = await openEventSocket(answer);
  try {
    await use(socket);
  } finally {
    socket.close();
  }
}

async function assertSilent(socket: EventSocket, id: string, what: string): Promise<void> {
  const heard = await socket.readEventsWithin(id, SILENCE_MS);
  assert.deepEqual(
    heard.map((event) => event.data),
    [],
    `${what} delivered events it should not have`,
  );
}

function assertDenied(answer: HandlerAnswer, expected: Record<number, string>): void {
  assert.deepEqual(
    Object.fromEntries(answer.denied.map(({ i, code }) => [i, code])),
    expected,
    `the handler answered ${JSON.stringify(answer)}`,
  );
  for (const i of Object.keys(expected)) {
    assert.ok(
      !answer.grants.some((grant) => grant.i === Number(i)),
      `op ${i} was both denied and granted`,
    );
  }
}

export const realtimeRuleAllowsCheck: Check = {
  title: "a subscribe its rule allows is granted, and receives what the server publishes there",
  run: async (ctx) => {
    const orderId = newId("o");
    const answer = await requestBatch(
      ctx,
      { connect: true, ops: [buildSubscribe("orders/:orderId", { orderId })] },
      { user: "ada", orders: [orderId] },
    );
    assert.deepEqual(answer.denied, []);
    const grant = readGrant(answer, 0);
    assert.equal(grant.wire, `/app/orders/${orderId}`);
    await runWithSocket(answer, async (socket) => {
      await socket.subscribe("order", grant.wire, grant.token);
      await assertPublishedFromServer(ctx, "orders/:orderId", { orderId }, { status: "shipped" });
      const event = await socket.readNextEvent("order");
      assert.equal(event.v, 1);
      assert.equal(event.ch, grant.wire);
      assert.equal(event.kind, "live");
      assert.match(event.id, /^[0-9a-f]{32}$/);
      assert.deepEqual(event.data, { status: "shipped" });
    });
  },
};

export const realtimeRuleDeniesCheck: Check = {
  title: "a subscribe its rule refuses is denied forbidden, and one from nobody unauthenticated",
  run: async (ctx) => {
    const [mine, theirs] = [newId("o"), newId("o")];
    const ops = [
      buildSubscribe("orders/:orderId", { orderId: mine }),
      buildSubscribe("orders/:orderId", { orderId: theirs }),
    ];
    const caller = { user: "ada", orders: [mine] };
    const answer = await requestBatch(ctx, { connect: true, ops }, caller);
    assertDenied(answer, { 1: "forbidden" });
    readGrant(answer, 0);
    assertDenied(await requestBatch(ctx, { connect: true, ops }), {
      0: "unauthenticated",
      1: "unauthenticated",
    });
  },
};

export const realtimePublicCheck: Check = {
  title:
    "a public channel is granted to nobody in particular, and receives what the server publishes",
  run: async (ctx) => {
    const answer = await requestBatch(ctx, { connect: true, ops: [buildSubscribe("status")] });
    assert.deepEqual(answer.denied, []);
    const grant = readGrant(answer, 0);
    assert.equal(grant.wire, "/app/status");
    const text = newId("status");
    await runWithSocket(answer, async (socket) => {
      await socket.subscribe("status", grant.wire, grant.token);
      await assertPublishedFromServer(ctx, "status", {}, { text });
      assert.deepEqual((await socket.readNextEvent("status")).data, { text });
    });
  },
};

export const realtimeWildcardCheck: Check = {
  title:
    "a wildcard subscribe leaving off a trailing param receives every channel under the rest, and nothing beyond",
  run: async (ctx) => {
    const [projectId, otherId] = [newId("p"), newId("p")];
    const pattern = "projects/:projectId/deploys/:deployId";
    const answer = await requestBatch(
      ctx,
      {
        connect: true,
        ops: [buildSubscribe(pattern, { projectId }), buildSubscribe("orders/:orderId")],
      },
      { user: "ada", projects: [projectId] },
    );
    assertDenied(answer, { 1: "missing-param" });
    const grant = readGrant(answer, 0);
    assert.equal(grant.wire, `/app/projects/${projectId}/deploys/*`);
    await runWithSocket(answer, async (socket) => {
      await socket.subscribe("deploys", grant.wire, grant.token);
      await assertPublishedFromServer(
        ctx,
        pattern,
        { projectId, deployId: "d-1" },
        { state: "one" },
      );
      await assertPublishedFromServer(
        ctx,
        pattern,
        { projectId: otherId, deployId: "d-1" },
        { state: "other" },
      );
      await assertPublishedFromServer(
        ctx,
        pattern,
        { projectId, deployId: "d-2" },
        { state: "two" },
      );
      const first = await socket.readNextEvent("deploys");
      const second = await socket.readNextEvent("deploys");
      assert.deepEqual(
        [first, second].map((event) => [event.ch, event.data]),
        [
          [`/app/projects/${projectId}/deploys/d-1`, { state: "one" }],
          [`/app/projects/${projectId}/deploys/d-2`, { state: "two" }],
        ],
      );
      await assertSilent(socket, "deploys", "a wildcard subscription on another project");
    });
  },
};

type ServerClock = () => number;

function newServerClock(live: DecodedToken): ServerClock {
  const receivedAt = performance.now();
  return () => live.claims.iat + Math.floor((performance.now() - receivedAt) / 1000);
}

type TokenToSign = DecodedToken & { signedBy: Signer };

async function signToken(ctx: CheckContext, name: string, token: TokenToSign): Promise<string> {
  const sent = await json(ctx, "/api/tokens", {
    method: "POST",
    headers: { "content-type": "application/json", [JOURNEY_NONCE_HEADER]: ctx.journeyNonce },
    body: JSON.stringify(token),
  });
  assert.equal(
    sent.res.status,
    200,
    `signing ${name} answered ${describeResponse(sent.res, sent.text)}`,
  );
  return (sent.body as { token: string }).token;
}

function signForgery(ctx: CheckContext, forgery: Forgery, serverSecond: ServerClock) {
  return signToken(ctx, forgery.name, {
    header: forgery.header,
    claims: forgery.claimsAt(serverSecond()),
    signedBy: forgery.signedBy,
  });
}

async function readRefusal(attempt: Promise<unknown>): Promise<string | undefined> {
  try {
    await attempt;
    return undefined;
  } catch (error) {
    return error instanceof SocketRefusal ? error.errorType : String(error);
  }
}

async function requestVectorGrants(ctx: CheckContext): Promise<HandlerAnswer> {
  const { expect } = readTokenVectors().tokens;
  const orderId = expect.ch.split("/").at(-1) ?? "";
  const answer = await requestBatch(
    ctx,
    { connect: true, ops: [buildSubscribe("orders/:orderId", { orderId })] },
    { user: "ada", orders: [orderId] },
  );
  assert.equal(
    readGrant(answer, 0).wire,
    expect.ch,
    "the vectors name another channel than the fixture's",
  );
  return answer;
}

function signUnchanged(ctx: CheckContext, live: DecodedToken): Promise<string> {
  return signToken(ctx, "the live token re-signed", { ...live, signedBy: "binding" });
}

function describeAccepted(forgery: Forgery, refusal: string | undefined): string | undefined {
  if (refusal === "UnauthorizedException") return undefined;
  return `${forgery.name} (${forgery.reason}): ${refusal ?? "accepted"}`;
}

export const realtimeConnectTokenVectorsCheck: Check = {
  title:
    "every bad token in the shared vectors is refused at connect, where the live token signed the same way opens",
  sendsJourneyNonce: true,
  run: async (ctx) => {
    const answer = await requestVectorGrants(ctx);
    const live = decodeToken(readConnectToken(answer));
    const serverSecond = newServerClock(live);
    const open = (token: string) =>
      readRefusal(
        EventSocket.open(answer.url, answer.host, token).then((socket) => socket.close()),
      );
    assert.equal(
      await open(await signUnchanged(ctx, live)),
      undefined,
      "the live token re-signed was refused",
    );
    const wrong: string[] = [];
    for (const forgery of forgeRefusedCases(readTokenVectors(), live)) {
      const accepted = describeAccepted(
        forgery,
        await open(await signForgery(ctx, forgery, serverSecond)),
      );
      if (accepted) wrong.push(accepted);
    }
    assert.deepEqual(wrong, [], "the transport did not refuse a bad connect token as unauthorized");
  },
};

export const realtimeSubscribeTokenVectorsCheck: Check = {
  title:
    "every bad token in the shared vectors is refused at subscribe, where the live token signed the same way subscribes",
  sendsJourneyNonce: true,
  run: async (ctx) => {
    const answer = await requestVectorGrants(ctx);
    const grant = readGrant(answer, 0);
    const live = decodeToken(grant.token);
    const serverSecond = newServerClock(live);
    await runWithSocket(answer, async (socket) => {
      const wrong: string[] = [];
      for (const [n, forgery] of forgeRefusedCases(readTokenVectors(), live).entries()) {
        const token = await signForgery(ctx, forgery, serverSecond);
        const accepted = describeAccepted(
          forgery,
          await readRefusal(socket.subscribe(`bad-${n}`, grant.wire, token)),
        );
        if (accepted) wrong.push(accepted);
      }
      assert.deepEqual(
        wrong,
        [],
        "the transport did not refuse a bad subscribe token as unauthorized",
      );
      await socket.subscribe("re-signed", grant.wire, await signUnchanged(ctx, live));
    });
  },
};

export const realtimeRelayedPublishCheck: Check = {
  title:
    "a browser publish its rule allows is relayed to subscribers, and one it refuses, or on a channel with no publish rule, is denied and reaches nobody",
  run: async (ctx) => {
    const roomId = newId("r");
    const orderId = newId("o");
    const listener = await requestBatch(
      ctx,
      { connect: true, ops: [buildSubscribe("rooms/:roomId", { roomId })] },
      { user: "ada" },
    );
    const grant = readGrant(listener, 0);
    await runWithSocket(listener, async (socket) => {
      await socket.subscribe("room", grant.wire, grant.token);
      const relayed = await requestBatch(
        ctx,
        { ops: [buildPublish("rooms/:roomId", { roomId }, { text: "relayed" })] },
        { user: "grace", publish: true },
      );
      assertDenied(relayed, {});
      assert.deepEqual(relayed.grants, [{ i: 0, wire: grant.wire }]);
      assert.equal(relayed.connect, undefined, "a publish alone was minted a connect token");
      const event = await socket.readNextEvent("room");
      assert.deepEqual([event.ch, event.data], [grant.wire, { text: "relayed" }]);

      const refused = await requestBatch(
        ctx,
        {
          ops: [
            buildPublish("rooms/:roomId", { roomId }, { text: "muted" }),
            buildPublish("orders/:orderId", { orderId }, { status: "forged" }),
          ],
        },
        { user: "mallory", orders: [orderId] },
      );
      assertDenied(refused, { 0: "forbidden", 1: "no-publish-rule" });
      assert.deepEqual(
        (
          await requestBatch(ctx, {
            ops: [buildPublish("rooms/:roomId", { roomId }, { text: "x" })],
          })
        ).denied,
        [{ i: 0, code: "unauthenticated" }],
      );
      await assertSilent(socket, "room", "a refused publish");
    });
  },
};

export const realtimeSchemaCheck: Check = {
  title:
    "an event its schema rejects is refused from a browser and from the server, and reaches nobody",
  run: async (ctx) => {
    const roomId = newId("r");
    const listener = await requestBatch(
      ctx,
      { connect: true, ops: [buildSubscribe("rooms/:roomId", { roomId })] },
      { user: "ada" },
    );
    const grant = readGrant(listener, 0);
    await runWithSocket(listener, async (socket) => {
      await socket.subscribe("room", grant.wire, grant.token);
      const relayed = await requestBatch(
        ctx,
        {
          ops: [
            buildPublish("rooms/:roomId", { roomId }, { text: 42 }),
            { op: "publish", pattern: "rooms/:roomId", params: { roomId } },
          ],
        },
        { user: "grace", publish: true },
      );
      assertDenied(relayed, { 0: "invalid-body", 1: "invalid-body" });
      const fromServer = await publishFromServer(
        ctx,
        "rooms/:roomId",
        { roomId },
        { words: "no text" },
      );
      assert.deepEqual(fromServer, { status: 422, code: "invalid-body" });
      await assertSilent(socket, "room", "a publish its schema rejected");
    });
  },
};

async function publishUntilHeard(
  ctx: CheckContext,
  orderId: string,
  heard: Map<string, string[]>,
  callers: string[],
  status: string,
): Promise<void> {
  const deadline = Date.now() + SETTLED_WITHIN_MS;
  while (!callers.every((caller) => heard.get(caller)?.includes(status))) {
    assert.ok(
      Date.now() < deadline,
      `${callers.filter((caller) => !heard.get(caller)?.includes(status)).join(", ")} never heard ${status}: ${JSON.stringify(Object.fromEntries(heard))}`,
    );
    await assertPublishedFromServer(ctx, "orders/:orderId", { orderId }, { status });
    await delay(250);
  }
}

class RecordedWebSocket extends WebSocket {
  static readonly opened: WebSocket[] = [];

  constructor(url: string | URL, protocols?: string | string[]) {
    super(url, protocols);
    RecordedWebSocket.opened.push(this);
  }
}

export const realtimeReauthorizeCheck: Check = {
  title:
    "after a reconnect every live subscription is authorized again: a caller who kept access hears the channel again, and a revoked one loses it",
  run: async (ctx) => {
    const orderId = newId("o");
    const callers: Record<string, Caller> = {
      kept: { user: "ada", orders: [orderId] },
      revoked: { user: "bob", orders: [orderId] },
    };
    const heard = new Map<string, string[]>();
    const failures = new Map<string, RealtimeError[]>();
    const native = globalThis.WebSocket;
    globalThis.WebSocket = RecordedWebSocket as typeof WebSocket;
    const clients = Object.entries(callers).map(([name, caller]) => {
      const client = createRealtimeClient<FixtureChannels>({
        url: `${ctx.baseUrl}${HANDLER_PATH}`,
        headers: () => ({ authorization: encodeBearer(caller) }),
      });
      client.subscribe(
        "orders/:orderId",
        {
          params: { orderId },
          onError: (error) => failures.set(name, [...(failures.get(name) ?? []), error]),
        },
        (event) => heard.set(name, [...(heard.get(name) ?? []), event.status]),
      );
      return client;
    });
    try {
      await publishUntilHeard(ctx, orderId, heard, ["kept", "revoked"], "before");
      callers.revoked.orders = [];
      const dropped = RecordedWebSocket.opened.splice(0);
      assert.equal(dropped.length, 2, "each client opened one socket");
      for (const socket of dropped) socket.close();

      const deadline = Date.now() + SETTLED_WITHIN_MS;
      while (!failures.get("revoked")?.some((error) => !error.retriable)) {
        assert.ok(
          Date.now() < deadline,
          `the revoked caller was never refused: ${JSON.stringify([...failures])}`,
        );
        await delay(100);
      }
      const final = failures.get("revoked")?.find((error) => !error.retriable);
      assert.equal(final?.code, "forbidden", `the revoked caller ended with ${final?.message}`);
      await publishUntilHeard(ctx, orderId, heard, ["kept"], "after");
      assert.ok(
        !heard.get("revoked")?.includes("after"),
        "the revoked caller heard the channel after its reconnect",
      );
    } finally {
      for (const client of clients) client.close();
      globalThis.WebSocket = native;
    }
  },
};

export const realtimeHandlerDefaultsCheck: Check = {
  title:
    "the handler takes POST of application/json alone from its own origin, answers no-store and sets no cookie, and sends no CORS header to another origin",
  run: async (ctx) => {
    const batch = { ops: [buildSubscribe("status")] };
    const own = new URL(ctx.baseUrl).origin;
    const answers = {
      get: await json(ctx, HANDLER_PATH),
      text: await json(ctx, HANDLER_PATH, {
        method: "POST",
        headers: { "content-type": "text/plain" },
        body: JSON.stringify(batch),
      }),
      elsewhere: await postBatch(ctx, batch, { origin: FOREIGN_ORIGIN }),
      own: await postBatch(ctx, batch, { origin: own }),
      noOrigin: await postBatch(ctx, batch),
      malformed: await json(ctx, HANDLER_PATH, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: "{",
      }),
    };
    assert.deepEqual(
      Object.fromEntries(Object.entries(answers).map(([name, sent]) => [name, sent.res.status])),
      { get: 405, text: 415, elsewhere: 403, own: 200, noOrigin: 200, malformed: 400 },
    );
    for (const [name, sent] of Object.entries(answers)) {
      const shown = describeResponse(sent.res, sent.text);
      assert.match(sent.res.headers.get("cache-control") ?? "", /no-store/, `${name}: ${shown}`);
      assert.equal(sent.res.headers.get("set-cookie"), null, `${name}: ${shown}`);
      const cors = [...sent.res.headers.keys()].filter((header) =>
        header.startsWith("access-control-"),
      );
      assert.deepEqual(cors, [], `${name} sent CORS headers: ${shown}`);
    }
    const preflight = await ctx.fetch(`${ctx.baseUrl}${HANDLER_PATH}`, {
      method: "OPTIONS",
      headers: { origin: FOREIGN_ORIGIN, "access-control-request-method": "POST" },
    });
    assert.equal(preflight.headers.get("access-control-allow-origin"), null);
    assert.notEqual(
      preflight.status,
      204,
      "a preflight from another origin was answered as allowed",
    );
  },
};

export const realtimeOperationLimitCheck: Check = {
  title: "a batch of 50 operations is answered for each of them, and one of 51 is refused whole",
  run: async (ctx) => {
    const ops = Array.from({ length: MAX_OPERATIONS + 1 }, () => buildSubscribe("status"));
    const full = await requestBatch(ctx, { ops: ops.slice(0, MAX_OPERATIONS) });
    assert.deepEqual(
      full.grants.map((grant) => grant.i),
      Array.from({ length: MAX_OPERATIONS }, (_, i) => i),
    );
    const over = await postBatch(ctx, { ops });
    assert.equal(over.res.status, 400, describeResponse(over.res, over.text));
  },
};

export const realtimeSubscriptionLimitCheck: Check = {
  title: "a connection holds 200 subscriptions and refuses the 201st",
  run: async (ctx) => {
    const rooms = Array.from({ length: MAX_SUBSCRIPTIONS + 1 }, () => newId("r"));
    const grants: { wire: string; token: string }[] = [];
    let first: HandlerAnswer | undefined;
    for (let at = 0; at < rooms.length; at += MAX_OPERATIONS) {
      const answer = await requestBatch(
        ctx,
        {
          connect: at === 0,
          ops: rooms
            .slice(at, at + MAX_OPERATIONS)
            .map((roomId) => buildSubscribe("rooms/:roomId", { roomId })),
        },
        { user: "ada" },
      );
      first ??= answer;
      grants.push(...answer.grants.map((grant) => readGrant(answer, grant.i)));
    }
    assert.equal(grants.length, rooms.length);
    await runWithSocket(first as HandlerAnswer, async (socket) => {
      for (const [n, grant] of grants.slice(0, MAX_SUBSCRIPTIONS).entries()) {
        await socket.subscribe(`room-${n}`, grant.wire, grant.token);
      }
      const last = grants[MAX_SUBSCRIPTIONS] as { wire: string; token: string };
      const refusal = await readRefusal(socket.subscribe("one-too-many", last.wire, last.token));
      assert.equal(refusal, "LimitExceededException");
    });
  },
};

function buildNoteOfBytes(roomId: string, bytes: number): { text: string } {
  const overhead = JSON.stringify({
    v: 1,
    id: "0".repeat(32),
    ch: `/app/rooms/${roomId}`,
    ts: Date.now(),
    kind: "live",
    data: { text: "" },
  }).length;
  return { text: "x".repeat(bytes - overhead) };
}

export const realtimeEventSizeCheck: Check = {
  title:
    "an event of 240 KB is delivered whole, and one a byte past it is refused from the server and from a browser",
  run: async (ctx) => {
    const roomId = newId("r");
    const listener = await requestBatch(
      ctx,
      { connect: true, ops: [buildSubscribe("rooms/:roomId", { roomId })] },
      { user: "ada" },
    );
    const grant = readGrant(listener, 0);
    await runWithSocket(listener, async (socket) => {
      await socket.subscribe("room", grant.wire, grant.token);
      const largest = buildNoteOfBytes(roomId, MAX_EVENT_BYTES);
      await assertPublishedFromServer(ctx, "rooms/:roomId", { roomId }, largest);
      assert.deepEqual((await socket.readNextEvent("room")).data, largest);

      const over = buildNoteOfBytes(roomId, MAX_EVENT_BYTES + 1);
      assert.deepEqual(await publishFromServer(ctx, "rooms/:roomId", { roomId }, over), {
        status: 422,
        code: "body-too-large",
      });
      const relayed = await requestBatch(
        ctx,
        { ops: [buildPublish("rooms/:roomId", { roomId }, over)] },
        { user: "grace", publish: true },
      );
      assertDenied(relayed, { 0: "body-too-large" });
      await assertSilent(socket, "room", "an event past the size limit");
    });
  },
};

export const realtimeChecks: Check[] = [
  realtimeRuleAllowsCheck,
  realtimeRuleDeniesCheck,
  realtimePublicCheck,
  realtimeWildcardCheck,
  realtimeConnectTokenVectorsCheck,
  realtimeSubscribeTokenVectorsCheck,
  realtimeRelayedPublishCheck,
  realtimeSchemaCheck,
  realtimeReauthorizeCheck,
  realtimeHandlerDefaultsCheck,
  realtimeOperationLimitCheck,
  realtimeSubscriptionLimitCheck,
  realtimeEventSizeCheck,
];
