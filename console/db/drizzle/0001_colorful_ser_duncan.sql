CREATE TABLE "resource_assignment" (
	"id" text PRIMARY KEY NOT NULL,
	"user_id" text NOT NULL,
	"project_id" text NOT NULL,
	"resource_name" text NOT NULL,
	"resource_type" text NOT NULL,
	"config" jsonb NOT NULL,
	"database_name" text NOT NULL,
	"role_name" text NOT NULL,
	"password" text NOT NULL,
	"created_at" timestamp DEFAULT now() NOT NULL,
	"updated_at" timestamp DEFAULT now() NOT NULL
);
--> statement-breakpoint
ALTER TABLE "resource_assignment" ADD CONSTRAINT "resource_assignment_user_id_user_id_fk" FOREIGN KEY ("user_id") REFERENCES "public"."user"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
ALTER TABLE "resource_assignment" ADD CONSTRAINT "resource_assignment_project_id_project_id_fk" FOREIGN KEY ("project_id") REFERENCES "public"."project"("id") ON DELETE cascade ON UPDATE no action;--> statement-breakpoint
CREATE UNIQUE INDEX "resource_assignment_reuse_key_uidx" ON "resource_assignment" USING btree ("user_id","project_id","resource_name","resource_type");