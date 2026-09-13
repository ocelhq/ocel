import { relations } from "drizzle-orm";
import { jsonb, pgEnum, pgTable, text, timestamp, uniqueIndex } from "drizzle-orm/pg-core";
import { organization } from "./auth-schema";
import { computeKind } from "./deployment";

export const CONNECTOR_REACHES = ["dial"] as const;
export type ConnectorReach = (typeof CONNECTOR_REACHES)[number];
export const connectorReach = pgEnum("connector_reach", CONNECTOR_REACHES);

export type ConnectorDenial = { verb: string; at: string; message: string };

export const connector = pgTable(
  "connector",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    target: text("target").notNull(),
    vendor: text("vendor").notNull(),
    compute: computeKind("compute"),
    reach: connectorReach("reach").notNull().default("dial"),
    url: text("url"),
    publicKey: text("public_key"),
    tlsPin: text("tls_pin"),
    version: text("version"),
    capabilities: jsonb("capabilities").$type<string[]>().notNull().default([]),
    connectedAt: timestamp("connected_at"),
    lastSeenAt: timestamp("last_seen_at"),
    lastDenied: jsonb("last_denied").$type<ConnectorDenial>(),
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
