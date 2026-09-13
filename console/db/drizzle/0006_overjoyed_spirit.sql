CREATE TYPE "public"."connector_reach" AS ENUM('dial');--> statement-breakpoint
CREATE TYPE "public"."compute_kind" AS ENUM('serverless', 'container');--> statement-breakpoint
CREATE TABLE "jwks" (
	"id" text PRIMARY KEY NOT NULL,
	"public_key" text NOT NULL,
	"private_key" text NOT NULL,
	"created_at" timestamp NOT NULL,
	"expires_at" timestamp
);
--> statement-breakpoint
ALTER TABLE "connector" ALTER COLUMN "vendor" SET DATA TYPE text;--> statement-breakpoint
ALTER TABLE "connector" ALTER COLUMN "url" DROP NOT NULL;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "compute" "compute_kind";--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "reach" "connector_reach" DEFAULT 'dial' NOT NULL;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "public_key" text;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "tls_pin" text;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "last_denied" jsonb;--> statement-breakpoint
ALTER TABLE "deployment" ADD COLUMN "target" text NOT NULL;--> statement-breakpoint
DROP TYPE "public"."connector_vendor";