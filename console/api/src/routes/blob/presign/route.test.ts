import { db } from "@console/db";
import { uploadSession } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "../../../../test/auth-harness";
import { setupTestDatabase } from "../../../../test/db";
import { createProject } from "../../projects/route";
import { presignUpload } from "./route";

async function createProjectFor(
  session: { headers: Headers },
  slug: string,
): Promise<{ id: string; organizationId: string }> {
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

function postRequest(body: unknown, headers: Headers) {
  return new Request("http://localhost/api/blob/presign", {
    method: "POST",
    headers: {
      ...Object.fromEntries(headers),
      "Content-Type": "application/json",
    },
    body: JSON.stringify(body),
  });
}

const encodedMetadata = Buffer.from(
  JSON.stringify({ uploader: "avatar", metadata: { userId: "u1" } }),
).toString("base64");

function presignBody(projectId: string) {
  return {
    projectId,
    bucket: "storage",
    files: [{ key: "avatar.png", name: "avatar.png", size: 2048, mimeType: "image/png" }],
    metadata: encodedMetadata,
    contentDisposition: "inline",
    callbackBaseUrl: "http://localhost:3000/api/upload",
  };
}

describe("POST /api/blob/presign", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("presigns: the app's own key back, a well-formed presigned PUT URL over the tenancy-namespaced object with bound conditions + session tag, and a persisted pending session", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "blob-presign-ok");

      const response = await presignUpload(postRequest(presignBody(created.id), session.headers));

      expect(response.status).toBe(200);
      const bodyJson = await response.json();

      const sessionId: string = bodyJson.sessionId;
      expect(sessionId).toBeTruthy();
      expect(bodyJson.files).toHaveLength(1);

      const target = bodyJson.files[0];
      expect(target.name).toBe("avatar.png");

      expect(target.key).toBe("avatar.png");

      const objectKey = `${created.organizationId}/${created.id}/${session.user.id}/avatar.png`;
      const url = new URL(target.url);
      expect(url.pathname).toContain(encodeURI(objectKey));
      expect(url.searchParams.get("X-Amz-Algorithm")).toBe("AWS4-HMAC-SHA256");
      expect(url.searchParams.get("X-Amz-Signature")).toBeTruthy();
      const signed = url.searchParams.get("X-Amz-SignedHeaders") ?? "";
      expect(signed).toContain("content-length");
      expect(signed).toContain("content-type");
      expect(signed).toContain("content-disposition");
      expect(target.contentDisposition).toBe("inline");
      expect(url.searchParams.get("x-amz-tagging")).toBe(`sessionId=${sessionId}`);

      const [row] = await db.select().from(uploadSession).where(eq(uploadSession.id, sessionId));
      expect(row).toBeTruthy();
      expect(row.userId).toBe(session.user.id);
      expect(row.projectId).toBe(created.id);
      expect(row.organizationId).toBe(created.organizationId);
      expect(row.bucket).toBe("storage");
      expect(row.secret.length).toBeGreaterThan(0);
      expect(row.callbackBaseUrl).toBe("http://localhost:3000/api/upload");
      expect(row.contentDisposition).toBe("inline");
      expect(row.metadata).toBe(encodedMetadata);
      const files = row.files as Array<{ key: string; objectKey: string; state: string }>;
      expect(files).toHaveLength(1);
      expect(files[0].key).toBe("avatar.png");
      expect(files[0].objectKey).toBe(objectKey);
      expect(files[0].state).toBe("pending");
      expect(row.expiresAt.getTime()).toBeGreaterThan(Date.now());
    } finally {
      await session.cleanup();
    }
  });

  it("rejects a key that would escape the tenancy namespace", async () => {
    const session = await createTestSessionWithOrganization();
    const refused = [
      ["a leading slash", "/evil.png"],
      ["a parent segment", "../evil.png"],
      ["a parent segment further in", "a/../../evil.png"],
      ["a current-directory segment", "./evil.png"],
      ["a backslash", "a\\evil.png"],
      ["an empty segment", "a//evil.png"],
      ["a trailing slash", "a/"],
      ["a percent-encoded parent segment", "..%2Fevil.png"],
      ["a percent-encoded separator around a parent segment", "a%2F..%2F..%2Fevil.png"],
      ["percent-encoding that does not decode", "a%zz.png"],
      ["a control character", "a\u0001evil.png"],
      ["a percent-encoded newline", "a%0Aevil.png"],
      ["an object key longer than the store holds", `${"a".repeat(1100)}.png`],
    ] as const;

    try {
      const created = await createProjectFor(session, "blob-presign-traversal");
      for (const [why, key] of refused) {
        const body = presignBody(created.id);
        body.files[0].key = key;
        const response = await presignUpload(postRequest(body, session.headers));
        expect(response.status, why).toBe(400);
      }
    } finally {
      await session.cleanup();
    }
  });

  it("takes a nested key the app chose and namespaces it under the caller", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "blob-presign-nested");
      const body = presignBody(created.id);
      body.files[0].key = "documents/2024/q1-report.pdf";

      const response = await presignUpload(postRequest(body, session.headers));

      expect(response.status).toBe(200);
      const target = (await response.json()).files[0];
      expect(target.key).toBe("documents/2024/q1-report.pdf");
      expect(new URL(target.url).pathname).toContain(
        encodeURI(
          `${created.organizationId}/${created.id}/${session.user.id}/documents/2024/q1-report.pdf`,
        ),
      );
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "blob-presign-unauthed");
      const response = await presignUpload(postRequest(presignBody(created.id), new Headers()));
      expect(response.status).toBe(401);
    } finally {
      await session.cleanup();
    }
  });

  it("returns 404 for a project in an org the caller doesn't belong to", async () => {
    const session = await createTestSessionWithOrganization();
    const otherSession = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "blob-presign-not-your-org");
      const response = await presignUpload(
        postRequest(presignBody(created.id), otherSession.headers),
      );
      expect(response.status).toBe(404);
    } finally {
      await session.cleanup();
      await otherSession.cleanup();
    }
  });

  it("returns 400 for an invalid body", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const response = await presignUpload(postRequest({ projectId: "" }, session.headers));
      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });
});
