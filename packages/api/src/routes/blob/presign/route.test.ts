import { db } from "@repo/db";
import { uploadSession } from "@repo/db/schema";
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
    files: [
      { key: "avatar.png", name: "avatar.png", size: 2048, mimeType: "image/png" },
    ],
    metadata: encodedMetadata,
    contentDisposition: "inline",
    callbackBaseUrl: "http://localhost:3000/api/upload",
  };
}

describe("POST /api/blob/presign", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("presigns: tenancy-prefixed honest key, a well-formed presigned PUT URL with bound conditions + session tag, and a persisted pending session", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const created = await createProjectFor(session, "blob-presign-ok");

      const response = await presignUpload(
        postRequest(presignBody(created.id), session.headers),
      );

      expect(response.status).toBe(200);
      const bodyJson = await response.json();

      const sessionId: string = bodyJson.sessionId;
      expect(sessionId).toBeTruthy();
      expect(bodyJson.files).toHaveLength(1);

      const target = bodyJson.files[0];
      expect(target.name).toBe("avatar.png");

      // Honest, tenancy-prefixed key: {orgId}/{projectId}/{userId}/<user key>.
      const expectedKey = `${created.organizationId}/${created.id}/${session.user.id}/avatar.png`;
      expect(target.key).toBe(expectedKey);

      // Well-formed presigned PUT URL: SigV4 query params, the prefixed key in
      // the path, and the bound conditions in the signed-headers set.
      const url = new URL(target.url);
      expect(url.pathname).toContain(encodeURI(expectedKey));
      expect(url.searchParams.get("X-Amz-Algorithm")).toBe("AWS4-HMAC-SHA256");
      expect(url.searchParams.get("X-Amz-Signature")).toBeTruthy();
      const signed = url.searchParams.get("X-Amz-SignedHeaders") ?? "";
      expect(signed).toContain("content-length");
      expect(signed).toContain("content-type");
      // The session tag stays a signed header so its value is bound.
      expect(signed).toContain("x-amz-tagging");

      // A pending session persisted with the secret, callbackBaseUrl, verbatim
      // metadata, and the prefixed per-file state.
      const [row] = await db
        .select()
        .from(uploadSession)
        .where(eq(uploadSession.id, sessionId));
      expect(row).toBeTruthy();
      expect(row.userId).toBe(session.user.id);
      expect(row.projectId).toBe(created.id);
      expect(row.organizationId).toBe(created.organizationId);
      expect(row.bucket).toBe("storage");
      expect(row.secret.length).toBeGreaterThan(0);
      expect(row.callbackBaseUrl).toBe("http://localhost:3000/api/upload");
      expect(row.contentDisposition).toBe("inline");
      // Opaque metadata bytes round-trip verbatim.
      expect(row.metadata).toBe(encodedMetadata);
      const files = row.files as Array<{ key: string; state: string }>;
      expect(files).toHaveLength(1);
      expect(files[0].key).toBe(expectedKey);
      expect(files[0].state).toBe("pending");
      expect(row.expiresAt.getTime()).toBeGreaterThan(Date.now());
    } finally {
      await session.cleanup();
    }
  });

  it("returns 401 when unauthenticated", async () => {
    const session = await createTestSessionWithOrganization();
    try {
      const created = await createProjectFor(session, "blob-presign-unauthed");
      const response = await presignUpload(
        postRequest(presignBody(created.id), new Headers()),
      );
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
      const response = await presignUpload(
        postRequest({ projectId: "" }, session.headers),
      );
      expect(response.status).toBe(400);
    } finally {
      await session.cleanup();
    }
  });
});
