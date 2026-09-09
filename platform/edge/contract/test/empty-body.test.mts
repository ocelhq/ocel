import { expect, it } from "vitest";

import { dropEmptyBodySentinel, EMPTY_BODY_HEADER } from "../src/empty-body.mjs";

it("restores the empty body a sentinel response stands for", async () => {
  for (const status of [200, 302, 307, 404, 405, 500]) {
    const dropped = dropEmptyBodySentinel(
      new Response("\n", {
        status,
        headers: { [EMPTY_BODY_HEADER]: "1", "x-custom": "kept" },
      }),
    );

    expect(dropped.status).toBe(status);
    expect(await dropped.text()).toBe("");
    expect(dropped.headers.get(EMPTY_BODY_HEADER)).toBeNull();
    expect(dropped.headers.get("x-custom")).toBe("kept");
  }
});

it("cancels the sentinel body rather than leaving it to be read", async () => {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode("\n"));
    },
    cancel() {
      cancelled = true;
    },
  });

  dropEmptyBodySentinel(new Response(body, { headers: { [EMPTY_BODY_HEADER]: "1" } }));

  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(cancelled).toBe(true);
});

it("hands back an unmarked response untouched", async () => {
  const response = new Response("hello", { headers: { "content-type": "text/plain" } });

  const same = dropEmptyBodySentinel(response);

  expect(same).toBe(response);
  expect(await same.text()).toBe("hello");
});
