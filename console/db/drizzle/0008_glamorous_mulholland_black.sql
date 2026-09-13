CREATE TYPE "public"."compute_kind" AS ENUM('serverless', 'container');--> statement-breakpoint
ALTER TABLE "connector" ADD COLUMN "compute" "compute_kind";--> statement-breakpoint
ALTER TABLE "connector" DROP COLUMN "form";--> statement-breakpoint
DROP TYPE "public"."connector_form";