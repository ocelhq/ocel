import { relations } from "drizzle-orm";
import { pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { organization, user } from "./auth-schema";

export const project = pgTable(
  "project",
  {
    // Generated in application code via the `uuidv7` package, not Postgres's
    // default v4 gen_random_uuid() or Better Auth's own id generator - this
    // table isn't Better-Auth-managed.
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    slug: text("slug").notNull(),
    description: text("description"),
    createdBy: text("created_by").references(() => user.id, {
      onDelete: "set null",
    }),
    createdAt: timestamp("created_at").defaultNow().notNull(),
    updatedAt: timestamp("updated_at")
      .defaultNow()
      .$onUpdate(() => new Date())
      .notNull(),
  },
  (table) => [
    // Slug is unique per organization, not globally unique like
    // organization.slug.
    uniqueIndex("project_organizationId_slug_uidx").on(
      table.organizationId,
      table.slug,
    ),
  ],
);

export const projectRelations = relations(project, ({ one }) => ({
  organization: one(organization, {
    fields: [project.organizationId],
    references: [organization.id],
  }),
  createdByUser: one(user, {
    fields: [project.createdBy],
    references: [user.id],
  }),
}));
