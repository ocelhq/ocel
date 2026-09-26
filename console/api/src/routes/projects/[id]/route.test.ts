import { auth } from "@console/auth/next";
import { db } from "@console/db";
import { project, projectEnvValue } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../route";
import { deleteProject, getProjectById, updateProject } from "./route";

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
      const valueId = crypto.randomUUID();
      await db.insert(projectEnvValue).values({
        id: valueId,
        projectId: created.id,
        key: "LOG_LEVEL",
        value: "debug",
      });

      const response = await deleteProject(deleteRequest(session.headers), created.id);

      expect(response.status).toBe(204);
      const [row] = await db.select().from(projectEnvValue).where(eq(projectEnvValue.id, valueId));
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

  it("leaves a Project in another org in place and answers 404", async () => {
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

function patchRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/projects/x", {
    method: "PATCH",
    headers: { ...Object.fromEntries(headers), "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

describe("updateProject", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("replaces the frameworks, dropping duplicates", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "frameworks-replace");
      const response = await updateProject(
        patchRequest({ frameworks: ["nextjs", "go", "nextjs"] }, session.headers),
        created.id,
      );

      expect(response.status).toBe(200);
      expect((await response.json()).frameworks).toEqual(["nextjs", "go"]);

      const cleared = await updateProject(
        patchRequest({ frameworks: [] }, session.headers),
        created.id,
      );
      expect((await cleared.json()).frameworks).toEqual([]);
    } finally {
      await session.cleanup();
    }
  });

  it("rejects a framework it does not know with 400", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "frameworks-unknown");
      const response = await updateProject(
        patchRequest({ frameworks: ["cobol"] }, session.headers),
        created.id,
      );

      expect(response.status).toBe(400);
      const [row] = await db.select().from(project).where(eq(project.id, created.id));
      expect(row.frameworks).toEqual([]);
    } finally {
      await session.cleanup();
    }
  });

  it("leaves a Project in another org untouched and answers 404", async () => {
    const owner = await createTestSessionWithOrganization();
    const stranger = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(owner, "frameworks-foreign");
      const response = await updateProject(
        patchRequest({ frameworks: ["go"] }, stranger.headers),
        created.id,
      );

      expect(response.status).toBe(404);
      const [row] = await db.select().from(project).where(eq(project.id, created.id));
      expect(row.frameworks).toEqual([]);
    } finally {
      await owner.cleanup();
      await stranger.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const response = await updateProject(patchRequest({ frameworks: [] }, new Headers()), "x");
    expect(response.status).toBe(401);
  });
});
