import { relations } from "drizzle-orm";
import { jsonb, pgTable, text, timestamp } from "drizzle-orm/pg-core";
import { organization, user } from "./auth-schema";
import { project } from "./project";

export const uploadSession = pgTable("upload_session", {
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
  bucket: text("bucket").notNull(),
  secret: text("secret").notNull(),
  callbackBaseUrl: text("callback_base_url").notNull(),
  contentDisposition: text("content_disposition").notNull().default(""),
  metadata: text("metadata").notNull(),
  files: jsonb("files").notNull(),
  createdAt: timestamp("created_at").defaultNow().notNull(),
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
