import { relations } from "drizzle-orm";
import { jsonb, pgEnum, pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { organization } from "./auth-schema";

export const CONNECTOR_VENDORS = ["aws", "gcp", "vps"] as const;
export type ConnectorVendor = (typeof CONNECTOR_VENDORS)[number];
export const connectorVendor = pgEnum("connector_vendor", CONNECTOR_VENDORS);

export const connector = pgTable(
  "connector",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    target: text("target").notNull(),
    vendor: connectorVendor("vendor").notNull(),
    // TODO(alpha): #1134 has the connector polling the console, so this column becomes a
    // public key and the console dials nothing.
    url: text("url").notNull(),
    version: text("version"),
    capabilities: jsonb("capabilities").$type<string[]>().notNull().default([]),
    connectedAt: timestamp("connected_at"),
    lastSeenAt: timestamp("last_seen_at"),
    createdAt: timestamp("created_at").defaultNow().notNull(),
  },
  (table) => [
    uniqueIndex("connector_organization_target_uidx").on(table.organizationId, table.target),
  ],
);

export type Connector = typeof connector.$inferSelect;

export const connectorRelations = relations(connector, ({ one }) => ({
  organization: one(organization, {
    fields: [connector.organizationId],
    references: [organization.id],
  }),
}));
