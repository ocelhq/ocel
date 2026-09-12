import { relations } from "drizzle-orm";
import { pgEnum, pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { organization, user } from "./auth-schema";

export const FRAMEWORKS = [
  "nextjs",
  "react",
  "astro",
  "remix",
  "nuxt",
  "sveltekit",
  "node",
  "express",
  "fastify",
  "hono",
  "bun",
  "deno",
  "go",
  "python",
  "django",
  "rust",
] as const;

export type Framework = (typeof FRAMEWORKS)[number];

export const framework = pgEnum("framework", FRAMEWORKS);

export const project = pgTable(
  "project",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    slug: text("slug").notNull(),
    description: text("description"),
    frameworks: framework("frameworks").array().notNull().default([]),
    createdBy: text("created_by").references(() => user.id, {
      onDelete: "set null",
    }),
    createdAt: timestamp("created_at").defaultNow().notNull(),
    updatedAt: timestamp("updated_at")
      .defaultNow()
      .$onUpdate(() => new Date())
      .notNull(),
  },
  (table) => [uniqueIndex("project_organizationId_slug_uidx").on(table.organizationId, table.slug)],
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
