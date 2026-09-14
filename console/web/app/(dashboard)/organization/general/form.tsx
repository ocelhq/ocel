"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authClient } from "@/lib/auth-client";
import type { Role } from "@/lib/roles";
import { labelType } from "@/lib/type";
import { Stamp } from "../../stamp";

type Organization = {
  id: string;
  name: string;
  slug: string;
  role: Role;
  administers: boolean;
};

function slugify(value: string) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function plural(n: number, noun: string) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

const section = "flex flex-col gap-5 px-5 py-6 md:px-8";

const hint = "text-xs text-muted-foreground";

export function GeneralForm({
  organization,
  members,
  projects,
  createdAt,
  now,
}: {
  organization: Organization;
  members: number;
  projects: number;
  createdAt: string;
  now: string;
}) {
  const router = useRouter();
  const [name, setName] = useState(organization.name);
  const [slug, setSlug] = useState(organization.slug);
  const [pending, setPending] = useState<"save" | "leave" | "delete" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [confirmSlug, setConfirmSlug] = useState("");

  const dirty = name.trim() !== organization.name || slug !== organization.slug;
  const busy = pending !== null;

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!name.trim() || !slug) {
      setError("Give the organization a name and a slug.");
      return;
    }
    setError(null);
    setSaved(false);
    setPending("save");
    const { error: saveError } = await authClient.organization.update({
      organizationId: organization.id,
      data: { name: name.trim(), slug },
    });
    setPending(null);
    if (saveError) {
      setError(saveError.message ?? "The change wasn’t saved. Check the slug and try again.");
      return;
    }
    setSaved(true);
    router.refresh();
  }

  async function leave() {
    setError(null);
    setPending("leave");
    const { error: leaveError } = await authClient.organization.leave({
      organizationId: organization.id,
    });
    if (leaveError) {
      setPending(null);
      setError(leaveError.message ?? "You’re still a member. Try again.");
      return;
    }
    router.replace("/");
    router.refresh();
  }

  async function remove() {
    setError(null);
    setPending("delete");
    const { error: deleteError } = await authClient.organization.delete({
      organizationId: organization.id,
    });
    if (deleteError) {
      setPending(null);
      setError(deleteError.message ?? "The organization wasn’t deleted. Try again.");
      return;
    }
    router.replace("/");
    router.refresh();
  }

  return (
    <div className="flex max-w-2xl flex-col divide-y divide-border border border-border">
      <form onSubmit={save} className={section} noValidate>
        <h2 className={labelType}>Identity</h2>
        <label className="flex flex-col gap-2">
          <span className="text-sm font-medium">Name</span>
          <Input
            name="name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            disabled={!organization.administers || busy}
            autoComplete="organization"
          />
        </label>
        <label className="flex flex-col gap-2">
          <span className="text-sm font-medium">Slug</span>
          <Input
            name="slug"
            value={slug}
            onChange={(event) => setSlug(slugify(event.target.value))}
            disabled={!organization.administers || busy}
            aria-describedby="slug-hint"
          />
          <span id="slug-hint" className={hint}>
            Lowercase letters, numbers and dashes. The CLI shows it when you pick an organization,
            so a change here shows up on your teammates’ next login.
          </span>
        </label>
        {organization.administers ? (
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={!dirty || busy}>
              {pending === "save" ? "Saving…" : "Save changes"}
            </Button>
            {saved && !dirty && (
              <span role="status" className="text-sm text-go">
                Saved
              </span>
            )}
          </div>
        ) : (
          <p className={hint}>Only owners and admins can rename an organization.</p>
        )}
      </form>

      <div className={section}>
        <h2 className={labelType}>Membership</h2>
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
          <dt className="text-muted-foreground">Your role</dt>
          <dd className="capitalize">{organization.role}</dd>
          <dt className="text-muted-foreground">Members</dt>
          <dd>
            <Link
              href="/organization/members"
              className="underline-offset-4 outline-none hover:underline focus-visible:underline"
            >
              {plural(members, "member")}
            </Link>
          </dd>
          <dt className="text-muted-foreground">Projects</dt>
          <dd>{plural(projects, "project")}</dd>
          <dt className="text-muted-foreground">Created</dt>
          <dd>
            <Stamp at={createdAt} now={now} />
          </dd>
        </dl>
        <div className="flex flex-col items-start gap-2">
          <Button type="button" variant="outline" disabled={busy} onClick={leave}>
            {pending === "leave" ? "Leaving…" : "Leave organization"}
          </Button>
          <p className={hint}>
            You lose access to its projects. An owner can invite you back. The only owner can’t
            leave.
          </p>
        </div>
      </div>

      {organization.role === "owner" && (
        <div className={section}>
          <h2 className={labelType}>Delete</h2>
          <p className="max-w-prose text-sm text-muted-foreground">
            Removes the organization, its members and every project record the console holds.
            Nothing in your cloud account is touched; your deployments keep running.
          </p>
          {deleting ? (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                remove();
              }}
              className="flex flex-col gap-3"
            >
              <label className="flex flex-col gap-2">
                <span className="text-sm font-medium">
                  Type <span className="font-mono text-[13px]">{organization.slug}</span> to confirm
                </span>
                <Input
                  value={confirmSlug}
                  onChange={(event) => setConfirmSlug(event.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                  className="font-mono"
                />
              </label>
              <div className="flex items-center gap-2">
                <Button
                  type="submit"
                  variant="destructive"
                  disabled={confirmSlug !== organization.slug || busy}
                >
                  {pending === "delete" ? "Deleting…" : "Delete organization"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setDeleting(false);
                    setConfirmSlug("");
                  }}
                >
                  Cancel
                </Button>
              </div>
            </form>
          ) : (
            <Button
              type="button"
              variant="destructive"
              disabled={busy}
              onClick={() => setDeleting(true)}
              className="self-start"
            >
              Delete organization
            </Button>
          )}
        </div>
      )}

      {error && (
        <p role="alert" className="px-5 py-4 text-sm text-destructive md:px-8">
          {error}
        </p>
      )}
    </div>
  );
}
