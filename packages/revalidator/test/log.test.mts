import { afterEach, expect, it, vi } from "vitest";

import { context, report } from "../src/log.mjs";
import { parseMessage, permittedHosts } from "../src/message.mjs";

const host = "abc123.lambda-url.us-east-1.on.aws";
const bypassToken = "s3cr3t-preview-mode-id";

const parsed = parseMessage(
  JSON.stringify({
    v: 1,
    url: `https://${host}/blog/post`,
    headers: { "x-prerender-revalidate": bypassToken, "x-forwarded-host": "example.com" },
    expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
    isrPrefix: "build-1",
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
  }),
  permittedHosts(host),
);
const message = parsed.ok ? parsed.message : undefined;

afterEach(() => vi.restoreAllMocks());

function captured(): string[] {
  const lines: string[] = [];
  vi.spyOn(console, "log").mockImplementation((line: string) => void lines.push(line));
  return lines;
}

it("emits one JSON line carrying the dedup ingredients and the outcome", () => {
  const lines = captured();

  report(context("msg-1", message!), { event: "RevalidateOk" });

  expect(lines).toHaveLength(1);
  expect(JSON.parse(lines[0]!)).toEqual({
    event: "RevalidateOk",
    messageId: "msg-1",
    isrPrefix: "build-1",
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
  });
});

it("never emits the message's headers or url", () => {
  const lines = captured();

  report(context("msg-1", message!), { event: "RevalidateExpectMiss", expected: "REVALIDATED", got: "STALE" });
  report(context("msg-1", message!), { event: "RevalidateFailed", reason: "status-not-ok", status: 500 });

  const emitted = lines.join("\n");
  expect(emitted).not.toContain(bypassToken);
  expect(emitted).not.toContain("x-prerender-revalidate");
  expect(emitted).not.toContain(host);
});

it("reports a record it could not parse by id alone", () => {
  const lines = captured();

  report(context("msg-2", null), { event: "RevalidateFailed", reason: "malformed" });

  expect(JSON.parse(lines[0]!)).toEqual({
    event: "RevalidateFailed",
    messageId: "msg-2",
    reason: "malformed",
  });
});
