CREATE TYPE "public"."connector_form" AS ENUM('service', 'lambda', 'cloud-run', 'container');--> statement-breakpoint
CREATE TYPE "public"."connector_reach" AS ENUM('dial');--> statement-breakpoint
ALTER TABLE "connector" ALTER COLUMN "url" DROP NOT NULL;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "form" "connector_form" NOT NULL;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "reach" "connector_reach" DEFAULT 'dial' NOT NULL;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "public_key" text;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "tls_pin" text;--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "last_denied" jsonb;--> statement-breakpoint
ALTER TABLE "deployment" ADD COLUMN "target" text NOT NULL;