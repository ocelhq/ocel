import { beforeAll, describe, expect, it } from "vitest";
import { auth } from "@/lib/auth";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { POST as createProject } from "../route";
import { GET } from "./route";

function getRequest(headers: Headers) {
  return new Request("http://localhost/api/projects/x", {
    headers,
  });
}

async function createProjectFor(
  session: {
    headers: Headers;
  },
  slug: string,
) {
  const response = await createProject(
    new Request("http://localhost/api/projects", {
      method: "POST",
      headers: {
        ...Object.fromEntries(session.headers),
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ name: "My Project", slug }),
    }),
  );
  return response.json();
}

describe("GET /api/projects/[id]", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("returns the Project for a member of its org", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "get-me");

      const response = await GET(getRequest(session.headers), {
        params: Promise.resolve({ id: created.id }),
      });

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body.id).toBe(created.id);
      expect(body.slug).toBe("get-me");
    } finally {
      await session.cleanup();
    }
  });

  it("returns the Project regardless of which org is active in the session", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "cross-active-org");

      // Same user, a second organization they also belong to - and switch
      // the session's active org to it. The project lives in the *first*
      // org, which is no longer active.
      const secondOrganization = await auth.api.createOrganization({
        body: { name: "Second Org", slug: `second-org-${crypto.randomUUID()}` },
        headers: session.headers,
      });
      await auth.api.setActiveOrganization({
        body: { organizationId: secondOrganization?.id },
        headers: session.headers,
      });

      try {
        const response = await GET(getRequest(session.headers), {
          params: Promise.resolve({ id: created.id }),
        });

        expect(response.status).toBe(200);
        const body = await response.json();
        expect(body.id).toBe(created.id);
      } finally {
        if (secondOrganization) {
          await auth.api.deleteOrganization({
            body: { organizationId: secondOrganization.id },
            headers: session.headers,
          });
        }
      }
    } finally {
      await session.cleanup();
    }
  });

  it("returns 404 for a non-existent id", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await GET(getRequest(session.headers), {
        params: Promise.resolve({ id: "00000000-0000-7000-8000-000000000000" }),
      });

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 404 for a Project in an org the caller doesn't belong to", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "not-your-org");

      const response = await GET(getRequest(otherSession.headers), {
        params: Promise.resolve({ id: created.id }),
      });

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "unauthed");

      const response = await GET(getRequest(new Headers()), {
        params: Promise.resolve({ id: created.id }),
      });

      expect(response.status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });
});
