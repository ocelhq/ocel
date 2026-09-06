import { relations } from "drizzle-orm";
import { pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { project } from "./project";

export const projectEnvValue = pgTable(
  "project_env_value",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id")
      .notNull()
      .references(() => project.id, { onDelete: "cascade" }),
    key: text("key").notNull(),
    // TODO(alpha): encrypt at rest before the console holds anything but dev values (#992).
    value: text("value").notNull(),
    createdAt: timestamp("created_at").defaultNow().notNull(),
    updatedAt: timestamp("updated_at")
      .defaultNow()
      .$onUpdate(() => new Date())
      .notNull(),
  },
  (table) => [uniqueIndex("project_env_value_key_uidx").on(table.projectId, table.key)],
);

export const projectEnvValueRelations = relations(projectEnvValue, ({ one }) => ({
  project: one(project, {
    fields: [projectEnvValue.projectId],
    references: [project.id],
  }),
}));
