import { and, eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { project } from "@/db/schema";
import { db } from "@/lib/db";
import { createTestSessionWithOrganization } from "../../../test/auth-harness";
import { setupTestDatabase } from "../../../test/db";
import { POST } from "./route";

function postRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/projects", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

describe("POST /api/projects", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("creates a Project in the caller's active org and returns 201", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await POST(
        postRequest(
          { name: "My Project", slug: "my-project", description: "desc" },
          session.headers,
        ),
      );

      expect(response.status).toBe(201);
      const body = await response.json();
      expect(body.name).toBe("My Project");
      expect(body.slug).toBe("my-project");
      expect(body.organizationId).toBe(session.organization.id);
      expect(body.createdBy).toBe(session.user.id);
      expect(body.id).toBeTruthy();

      const [row] = await db
        .select()
        .from(project)
        .where(
          and(
            eq(project.organizationId, session.organization.id),
            eq(project.slug, "my-project"),
          ),
        );
      expect(row).toBeTruthy();
      expect(row.name).toBe("My Project");
    } finally {
      await session.cleanup();
    }
  });

  it("returns 400 for an invalid slug", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await POST(
        postRequest(
          { name: "My Project", slug: "Not A Slug!" },
          session.headers,
        ),
      );

      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 400 for an empty name", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await POST(
        postRequest({ name: "", slug: "my-project" }, session.headers),
      );

      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 409 on a duplicate slug within the same org", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const first = await POST(
        postRequest({ name: "First", slug: "dup-slug" }, session.headers),
      );
      expect(first.status).toBe(201);

      const second = await POST(
        postRequest({ name: "Second", slug: "dup-slug" }, session.headers),
      );
      expect(second.status).toBe(409);
    } finally {
      await session.cleanup();
    }
  });

  it("allows two different orgs to reuse the same slug", async () => {
    const sessionA = await createTestSessionWithOrganization();
    const sessionB = await createTestSessionWithOrganization();

    try {
      const responseA = await POST(
        postRequest(
          { name: "Org A Project", slug: "shared-slug" },
          sessionA.headers,
        ),
      );
      const responseB = await POST(
        postRequest(
          { name: "Org B Project", slug: "shared-slug" },
          sessionB.headers,
        ),
      );

      expect(responseA.status).toBe(201);
      expect(responseB.status).toBe(201);
    } finally {
      await sessionA.cleanup();
      await sessionB.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const response = await POST(
      postRequest({ name: "My Project", slug: "my-project" }, new Headers()),
    );

    expect(response.status).toBe(401);
  });
});
