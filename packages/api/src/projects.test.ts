import { db } from "@repo/db";
import { project } from "@repo/db/schema";
import { and, eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../test/auth-harness";
import { setupTestDatabase } from "../test/db";
import { createProject, listProjects } from "./projects";

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

function getRequest(headers: Headers) {
  return new Request("http://localhost/api/projects", { headers });
}

describe("POST /api/projects", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("creates a Project in the caller's active org and returns 201", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await createProject(
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
      const response = await createProject(
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
      const response = await createProject(
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
      const first = await createProject(
        postRequest({ name: "First", slug: "dup-slug" }, session.headers),
      );
      expect(first.status).toBe(201);

      const second = await createProject(
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
      const responseA = await createProject(
        postRequest(
          { name: "Org A Project", slug: "shared-slug" },
          sessionA.headers,
        ),
      );
      const responseB = await createProject(
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
    const response = await createProject(
      postRequest({ name: "My Project", slug: "my-project" }, new Headers()),
    );

    expect(response.status).toBe(401);
  });
});

describe("GET /api/projects", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("lists every Project in the caller's active org and none from another org", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();

    try {
      await createProject(
        postRequest({ name: "Alpha", slug: "alpha" }, session.headers),
      );
      await createProject(
        postRequest({ name: "Beta", slug: "beta" }, session.headers),
      );
      await createProject(
        postRequest(
          { name: "Not Mine", slug: "not-mine" },
          otherSession.headers,
        ),
      );

      const response = await listProjects(getRequest(session.headers));

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body).toHaveLength(2);
      expect(body.map((p: { slug: string }) => p.slug).sort()).toEqual([
        "alpha",
        "beta",
      ]);
      expect(
        body.every(
          (p: { organizationId: string }) =>
            p.organizationId === session.organization.id,
        ),
      ).toBe(true);
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });

  it("returns an empty collection when the active org has no Projects", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const response = await listProjects(getRequest(session.headers));

      expect(response.status).toBe(200);
      const body = await response.json();
      expect(body).toEqual([]);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const response = await listProjects(getRequest(new Headers()));

    expect(response.status).toBe(401);
  });
});
