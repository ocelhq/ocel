CREATE TYPE "public"."framework" AS ENUM('nextjs', 'react', 'astro', 'remix', 'nuxt', 'sveltekit', 'node', 'express', 'fastify', 'hono', 'bun', 'deno', 'go', 'python', 'django', 'rust');--> statement-breakpoint
CREATE TABLE "project_env_value" (
	"id" text PRIMARY KEY NOT NULL,
	"project_id" text NOT NULL,
	"key" text NOT NULL,
	"value" text NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "project" ADD COLUMN "frameworks" "framework"[] DEFAULT '{}' NOT NULL;--> statement-breakpoint
ALTER TABLE "project_env_value" ADD CONSTRAINT "project_env_value_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "project_env_value_key_uidx" ON "project_env_value" USING btree ("project_id","key");