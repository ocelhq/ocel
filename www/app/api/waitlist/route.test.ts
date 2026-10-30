import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/infra/env", () => ({
  env: { resend: { RESEND_API_KEY: "re_secret", RESEND_SEGMENT_ID: "seg_1" } },
}));

describe("POST /api/waitlist", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("adds the sign-up to Resend with the secret ocel delivers, not one in process.env", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_PROJECT_TOKEN", "phc_x");
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_HOST", "https://e.example.com");
    const fetch = vi.fn(
      async (_url: RequestInfo | URL, _init?: RequestInit) => new Response("{}", { status: 200 }),
    );
    vi.stubGlobal("fetch", fetch);
    const { POST } = await import("./route");

    const res = await POST(
      new Request("https://ocel.dev/api/waitlist", {
        method: "POST",
        body: JSON.stringify({ email: "ada@example.com", source: "landing" }),
      }),
    );

    expect(res.status).toBe(200);
    const resend = fetch.mock.calls.find(
      ([url]) => String(url) === "https://api.resend.com/contacts",
    );
    expect(resend?.[1]).toMatchObject({ headers: { authorization: "Bearer re_secret" } });
    expect(JSON.parse(String(resend?.[1]?.body))).toMatchObject({ segments: [{ id: "seg_1" }] });
  });
});
