import { describe, expect, it } from "bun:test";
import { deletePackages, type GitHubIo } from "./github";
import type { Package } from "./packages";

type Asked = { method: string; url: string; authorization: string | null };

const NOW = Date.parse("2026-09-24T10:00:00Z");

function github(answers: Record<string, Response | Response[]>) {
  const asked: Asked[] = [];
  const slept: number[] = [];
  const api = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    asked.push({ method, url, authorization: new Headers(init?.headers).get("authorization") });
    const answer = answers[`${method} ${url}`];
    const next = Array.isArray(answer) ? (answer.length > 1 ? answer.shift() : answer[0]) : answer;
    return next?.clone() ?? new Response(null, { status: 204 });
  }) as typeof fetch;
  const io: GitHubIo = {
    api,
    sleep: async (ms) => {
      slept.push(ms);
    },
    now: () => NOW + slept.reduce((sum, ms) => sum + ms, 0),
    random: () => 0.5,
  };
  return { io, asked, slept };
}

const PACKAGE = "https://api.github.com/orgs/ocelhq/packages/container/journey-vps%2Fweb";
const WEB: Package = { org: "ocelhq", name: "journey-vps/web", deployed: false };
const DEPLOYED: Package = { ...WEB, deployed: true };
const PRESENT = Response.json({ name: "journey-vps/web" });
const ABSENT = new Response('{"message":"Package not found."}', { status: 404 });

function answered(status: number, headers: Record<string, string> = {}): Response {
  return new Response(`{"message":"answered ${status}"}`, { status, headers });
}

const said = (asked: Asked[]) => asked.map((one) => `${one.method} ${one.url}`);

describe("deletePackages", () => {
  it("deletes the whole package the slash in its name is escaped in", async () => {
    const { io, asked } = github({ [`GET ${PACKAGE}`]: PRESENT });
    await deletePackages([WEB], "t0ken", io);
    expect(said(asked)).toEqual([`GET ${PACKAGE}`, `DELETE ${PACKAGE}`]);
    expect(asked.map((one) => one.authorization)).toEqual(["Bearer t0ken", "Bearer t0ken"]);
  });

  it("leaves alone a package no deploy of this run pushed and ghcr does not have", async () => {
    const { io, asked } = github({ [`GET ${PACKAGE}`]: ABSENT });
    await deletePackages([WEB], "t0ken", io);
    expect(said(asked)).toEqual([`GET ${PACKAGE}`]);
  });

  it("fails when a package a deploy of this run pushed through is not where it is named", async () => {
    const { io } = github({ [`GET ${PACKAGE}`]: ABSENT });
    await expect(deletePackages([DEPLOYED], "t0ken", io)).rejects.toThrow(
      /ocelhq\/journey-vps\/web/,
    );
  });

  it("counts a package gone before its deletion answered as deleted", async () => {
    const { io } = github({ [`GET ${PACKAGE}`]: PRESENT, [`DELETE ${PACKAGE}`]: ABSENT });
    await deletePackages([DEPLOYED], "t0ken", io);
  });

  it("fails loudly when the package refuses a deletion", async () => {
    const { io } = github({
      [`GET ${PACKAGE}`]: PRESENT,
      [`DELETE ${PACKAGE}`]: new Response('{"message":"Must have admin rights"}', { status: 403 }),
    });
    await expect(deletePackages([WEB], "t0ken", io)).rejects.toThrow(/403/);
  });
});

describe("a throttled or failing GitHub", () => {
  it("waits exactly as long as a Retry-After asks before asking again", async () => {
    const { io, asked, slept } = github({
      [`GET ${PACKAGE}`]: [answered(429, { "retry-after": "7" }), PRESENT],
    });
    await deletePackages([WEB], "t0ken", io);
    expect(slept).toEqual([7_000]);
    expect(said(asked)).toEqual([`GET ${PACKAGE}`, `GET ${PACKAGE}`, `DELETE ${PACKAGE}`]);
  });

  it("waits until the moment a Retry-After names as a date", async () => {
    const { io, slept } = github({
      [`GET ${PACKAGE}`]: [
        answered(503, { "retry-after": new Date(NOW + 12_000).toUTCString() }),
        PRESENT,
      ],
    });
    await deletePackages([WEB], "t0ken", io);
    expect(slept).toEqual([12_000]);
  });

  it("reads a 403 that sends a Retry-After as a throttle", async () => {
    const { io, slept } = github({
      [`GET ${PACKAGE}`]: [answered(403, { "retry-after": "3" }), PRESENT],
    });
    await deletePackages([WEB], "t0ken", io);
    expect(slept).toEqual([3_000]);
  });

  it("waits out a spent rate limit until the reset it names", async () => {
    const reset = String((NOW + 30_000) / 1_000);
    const { io, slept } = github({
      [`DELETE ${PACKAGE}`]: [
        answered(403, { "x-ratelimit-remaining": "0", "x-ratelimit-reset": reset }),
        new Response(null, { status: 204 }),
      ],
      [`GET ${PACKAGE}`]: PRESENT,
    });
    await deletePackages([WEB], "t0ken", io);
    expect(slept).toEqual([30_000]);
  });

  it("never asks again after a 403 that is no throttle", async () => {
    const { io, asked, slept } = github({
      [`GET ${PACKAGE}`]: answered(403, { "x-ratelimit-remaining": "4999" }),
    });
    await expect(deletePackages([WEB], "t0ken", io)).rejects.toThrow(/403/);
    expect(asked).toHaveLength(1);
    expect(slept).toEqual([]);
  });

  it("backs off with jitter on a failing server, and gives up after a bounded number of asks", async () => {
    const { io, asked, slept } = github({ [`GET ${PACKAGE}`]: answered(503) });
    await expect(deletePackages([WEB], "t0ken", io)).rejects.toThrow(/503/);
    expect(asked).toHaveLength(5);
    expect(slept).toEqual([750, 1_500, 3_000, 6_000]);
  });

  it("fails at once rather than wait out a Retry-After past its ceiling", async () => {
    const { io, asked, slept } = github({
      [`GET ${PACKAGE}`]: answered(429, { "retry-after": "3600" }),
    });
    await expect(deletePackages([WEB], "t0ken", io)).rejects.toThrow(/429/);
    expect(asked).toHaveLength(1);
    expect(slept).toEqual([]);
  });

  it("stops waiting once the waits it was asked for would pass its budget", async () => {
    const { io, slept } = github({
      [`GET ${PACKAGE}`]: answered(429, { "retry-after": "50" }),
    });
    await expect(deletePackages([WEB], "t0ken", io)).rejects.toThrow(/429/);
    expect(slept).toEqual([50_000, 50_000]);
  });
});
