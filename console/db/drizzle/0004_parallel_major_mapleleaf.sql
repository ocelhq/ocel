CREATE TYPE "public"."deployment_kind" AS ENUM('deploy', 'preview-up', 'preview-rm', 'rollback', 'destroy');--> statement-breakpoint
CREATE TYPE "public"."deployment_outcome" AS ENUM('succeeded', 'failed');--> statement-breakpoint
CREATE TYPE "public"."environment_class" AS ENUM('production', 'preview');--> statement-breakpoint
CREATE TABLE "deployment" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"run_id" text NOT NULL,
	"kind" "deployment_kind" NOT NULL,
	"environment_class" "environment_class" NOT NULL,
	"environment_identity" text DEFAULT '' NOT NULL,
	"promotion_id" text,
	"tag" text,
	"provider_name" text NOT NULL,
	"provider_region" text,
	"edge_kind" text,
	"outcome" "deployment_outcome" NOT NULL,
	"error" text,
	"trigger" jsonb NOT NULL,
	"git" jsonb,
	"cli_version" text,
	"started_at" timestamp,
	"deployed_at" timestamp NOT NULL,
	"trace" jsonb NOT NULL,
	"topology" jsonb NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "deployment" ADD CONSTRAINT "deployment_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "deployment_run_uidx" ON "deployment" USING btree ("project_id","run_id");--> statement-breakpoint
CREATE INDEX "deployment_project_latest_idx" ON "deployment" USING btree ("project_id","environment_class","deployed_at");