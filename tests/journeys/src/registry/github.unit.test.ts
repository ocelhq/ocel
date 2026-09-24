import { describe, expect, it } from "bun:test";
import { deletePackages } from "./github";
import type { Package } from "./packages";

type Asked = { method: string; url: string; authorization: string | null };

function restApi(answers: Record<string, Response>): { api: typeof fetch; asked: Asked[] } {
  const asked: Asked[] = [];
  const api = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    asked.push({ method, url, authorization: new Headers(init?.headers).get("authorization") });
    return answers[`${method} ${url}`]?.clone() ?? new Response(null, { status: 204 });
  }) as typeof fetch;
  return { api, asked };
}

const PACKAGE = "https://api.github.com/orgs/ocelhq/packages/container/journey-vps%2Fweb";
const WEB: Package = { org: "ocelhq", name: "journey-vps/web", deployed: false };
const DEPLOYED: Package = { ...WEB, deployed: true };
const HELD = Response.json({ name: "journey-vps/web" });
const ABSENT = new Response('{"message":"Package not found."}', { status: 404 });

const said = (asked: Asked[]) => asked.map((one) => `${one.method} ${one.url}`);

describe("deletePackages", () => {
  it("deletes the whole package the slash in its name is escaped in", async () => {
    const { api, asked } = restApi({ [`GET ${PACKAGE}`]: HELD });
    await deletePackages([WEB], "t0ken", api);
    expect(said(asked)).toEqual([`GET ${PACKAGE}`, `DELETE ${PACKAGE}`]);
    expect(asked.map((one) => one.authorization)).toEqual(["Bearer t0ken", "Bearer t0ken"]);
  });

  it("leaves alone a package no deploy of this run pushed and ghcr does not hold", async () => {
    const { api, asked } = restApi({ [`GET ${PACKAGE}`]: ABSENT });
    await deletePackages([WEB], "t0ken", api);
    expect(said(asked)).toEqual([`GET ${PACKAGE}`]);
  });

  it("fails when a package a deploy of this run pushed through is not where it is named", async () => {
    const { api } = restApi({ [`GET ${PACKAGE}`]: ABSENT });
    await expect(deletePackages([DEPLOYED], "t0ken", api)).rejects.toThrow(
      /ocelhq\/journey-vps\/web/,
    );
  });

  it("counts a package gone before its deletion answered as deleted", async () => {
    const { api } = restApi({ [`GET ${PACKAGE}`]: HELD, [`DELETE ${PACKAGE}`]: ABSENT });
    await deletePackages([DEPLOYED], "t0ken", api);
  });

  it("fails loudly when the package refuses a deletion", async () => {
    const { api } = restApi({
      [`GET ${PACKAGE}`]: HELD,
      [`DELETE ${PACKAGE}`]: new Response('{"message":"Must have admin rights"}', { status: 403 }),
    });
    await expect(deletePackages([WEB], "t0ken", api)).rejects.toThrow(/403/);
  });
});
