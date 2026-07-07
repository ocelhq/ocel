import { relations } from "drizzle-orm";
import { jsonb, pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { project } from "./project";
import { user } from "./auth-schema";

export const resourceAssignment = pgTable(
  "resource_assignment",
  {
    // Generated in application code via the `uuidv7` package, like `project`.
    id: text("id").primaryKey(),
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    // The name/type the app declared it under, e.g. postgres("main") ->
    // resourceName "main", resourceType "POSTGRES".
    resourceName: text("resource_name").notNull(),
    resourceType: text("resource_type").notNull(),
    // The declared config the app passed (e.g. { version: "17" }), stored so
    // a future slice can detect drift between the declaration and the row.
    config: jsonb("config").notNull(),
    databaseName: text("database_name").notNull(),
    roleName: text("role_name").notNull(),
    // Plaintext dev-only stand-in for SSM - see the epic's design decisions
    // (ocelhq-amu). TODO(prod): move to SSM once a real cloud target exists.
    password: text("password").notNull(),
    createdAt: timestamp("created_at").defaultNow().notNull(),
    updatedAt: timestamp("updated_at")
      .defaultNow()
      .$onUpdate(() => new Date())
      .notNull(),
  },
  (table) => [
    // The reuse key: an existing row for this tuple means resolve can reuse
    // it (this slice) instead of provisioning a new role+db (ocelhq-amu.3).
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
