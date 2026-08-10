import { relations } from "drizzle-orm";
import { jsonb, pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { project } from "./project";
import { user } from "./auth-schema";

export const resourceAssignment = pgTable(
  "resource_assignment",
  {
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    resourceName: text("resource_name").notNull(),
    resourceType: text("resource_type").notNull(),
    config: jsonb("config").notNull(),
    databaseName: text("database_name").notNull(),
    roleName: text("role_name").notNull(),
    // (ocelhq-amu). TODO(prod): move to SSM once a real cloud target exists.
    password: text("password").notNull(),
    createdAt: timestamp("created_at").defaultNow().notNull(),
    updatedAt: timestamp("updated_at")
      .defaultNow()
      .$onUpdate(() => new Date())
      .notNull(),
  },
  (table) => [
    uniqueIndex("resource_assignment_reuse_key_uidx").on(
      table.userId,
      table.projectId,
      table.resourceName,
      table.resourceType,
    ),
  ],
);

export const resourceAssignmentRelations = relations(
  resourceAssignment,
  ({ one }) => ({
    user: one(user, {
      fields: [resourceAssignment.userId],
      references: [user.id],
    }),
    project: one(project, {
      fields: [resourceAssignment.projectId],
      references: [project.id],
    }),
  }),
);
