CREATE TYPE "public"."connector_vendor" AS ENUM('aws', 'gcp', 'vps');--> statement-breakpoint
CREATE TABLE "connector" (
	"id" text PRIMARY KEY NOT NULL,
	"organization_id" text NOT NULL,
	"target" text NOT NULL,
	"vendor" "connector_vendor" NOT NULL,
	"url" text NOT NULL,
	"version" text,
	"capabilities" jsonb DEFAULT '[]'::jsonb NOT NULL,
	"connected_at" timestamp,
	"last_seen_at" timestamp,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "connector" ADD CONSTRAINT "connector_organization_id_organization_id_fk" FOREIGN KEY ("organization_id") REFERENCES "public"."organization"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "connector_organization_target_uidx" ON "connector" USING btree ("organization_id","target");