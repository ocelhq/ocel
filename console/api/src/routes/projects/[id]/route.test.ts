import { auth } from "@console/auth/next";
import { db } from "@console/db";
import { project, uploadSession } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../route";
import { deleteProject, getProjectById } from "./route";

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
): Promise<{ id: string; slug: string }> {
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
  return response.json() as Promise<{ id: string; slug: string }>;
}

describe("getProjectById", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("returns the Project for a member of its org", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "get-me");

      const response = await getProjectById(getRequest(session.headers), created.id);

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

      const secondOrganization = await auth.api.createOrganization({
        body: { name: "Second Org", slug: `second-org-${crypto.randomUUID()}` },
        headers: session.headers,
      });
      await auth.api.setActiveOrganization({
        body: { organizationId: secondOrganization?.id },
        headers: session.headers,
      });

      try {
        const response = await getProjectById(getRequest(session.headers), created.id);

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
      const response = await getProjectById(
        getRequest(session.headers),
        "00000000-0000-7000-8000-000000000000",
      );

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

      const response = await getProjectById(getRequest(otherSession.headers), created.id);

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

      const response = await getProjectById(getRequest(new Headers()), created.id);

      expect(response.status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });
});

function deleteRequest(headers: Headers) {
  return new Request("http://localhost/api/projects/x", {
    method: "DELETE",
    headers,
  });
}

describe("deleteProject", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("removes the Project from the active org and returns 204", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "delete-me");

      const response = await deleteProject(deleteRequest(session.headers), created.id);

      expect(response.status).toBe(204);
      const [row] = await db.select().from(project).where(eq(project.id, created.id));
      expect(row).toBeUndefined();
    } finally {
      await session.cleanup();
    }
  });

  it("takes the rows that hang off the Project with it", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "delete-with-children");
      const uploadId = crypto.randomUUID();
      await db.insert(uploadSession).values({
        id: uploadId,
        organizationId: session.organization.id,
        projectId: created.id,
        userId: session.user.id,
        bucket: "journeys",
        secret: "s",
        callbackBaseUrl: "http://localhost",
        metadata: "{}",
        files: [],
        expiresAt: new Date(Date.now() + 60_000),
      });

      const response = await deleteProject(deleteRequest(session.headers), created.id);

      expect(response.status).toBe(204);
      const [row] = await db.select().from(uploadSession).where(eq(uploadSession.id, uploadId));
      expect(row).toBeUndefined();
    } finally {
      await session.cleanup();
    }
  });

  it("returns 404 for a non-existent id", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await deleteProject(
        deleteRequest(session.headers),
        "00000000-0000-7000-8000-000000000000",
      );

      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
    }
  });

  it("leaves a Project in another org standing and answers 404", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "not-yours-to-delete");

      const response = await deleteProject(deleteRequest(otherSession.headers), created.id);

      expect(response.status).toBe(404);
      const [row] = await db.select().from(project).where(eq(project.id, created.id));
      expect(row).toBeTruthy();
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "unauthed-delete");

      const response = await deleteProject(deleteRequest(new Headers()), created.id);

      expect(response.status).toBe(401);
      const [row] = await db.select().from(project).where(eq(project.id, created.id));
      expect(row).toBeTruthy();
    } finally {
      await session.cleanup();
    }
  });
});
