import { relations } from "drizzle-orm";
import { jsonb, pgTable, text, timestamp } from "drizzle-orm/pg-core";
import { organization, user } from "./auth-schema";
import { project } from "./project";

// One ocel/blob upload session: a batch of N files presigned together, with
// completion tracked per file. Written pending at presign; the detector
// transitions each file pending -> succeeded | expired.
export const uploadSession = pgTable("upload_session", {
  // The session id (uuidv7), also the value bound into each presigned PUT's
  // signed x-amz-tagging so the detector can load the session from the object.
  id: text("id").primaryKey(),
  organizationId: text("organization_id")
    .notNull()
    .references(() => organization.id, { onDelete: "cascade" }),
  projectId: text("project_id")
    .notNull()
    .references(() => project.id, { onDelete: "cascade" }),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
  // The logical bucket name the app declared (bucket("storage") -> "storage").
  bucket: text("bucket").notNull(),
  // Per-session HMAC secret. Never leaves the store: completion callbacks are
  // verified via VerifyUploadSignature, so the env-blind route never holds it.
  secret: text("secret").notNull(),
  // The route origin+pathname captured at presign; the detector appends
  // ?op=callback to reach the same route.
  callbackBaseUrl: text("callback_base_url").notNull(),
  contentDisposition: text("content_disposition").notNull().default(""),
  // Opaque SDK-encoded metadata bytes (base64), stored verbatim and returned
  // unchanged on VerifyUploadSignature. Never interpreted here.
  metadata: text("metadata").notNull(),
  // Per-file state: [{ key, name, size, mimeType, state }] where key is the
  // honest (tenancy-prefixed) object key and state is pending|succeeded|expired.
  files: jsonb("files").notNull(),
  createdAt: timestamp("created_at").defaultNow().notNull(),
  // Strictly greater than the presigned PUT validity so the expiry sweep never
  // races a still-live URL.
  expiresAt: timestamp("expires_at").notNull(),
});

export const uploadSessionRelations = relations(uploadSession, ({ one }) => ({
  organization: one(organization, {
    fields: [uploadSession.organizationId],
    references: [organization.id],
  }),
  project: one(project, {
    fields: [uploadSession.projectId],
    references: [project.id],
  }),
  user: one(user, {
    fields: [uploadSession.userId],
    references: [user.id],
  }),
}));
