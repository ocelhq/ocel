"use client";

import { ArrowRightIcon } from "@phosphor-icons/react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authClient } from "@/lib/auth-client";
import { labelType } from "@/lib/type";

type Membership = { id: string; name: string; slug: string };

function slugify(value: string) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

export function NewOrganizationForm({
  destination,
  memberships,
  hasActiveOrganization,
}: {
  destination: string;
  memberships: Membership[];
  hasActiveOrganization: boolean;
}) {
  const router = useRouter();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const effectiveSlug = slugEdited ? slug : slugify(name);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!name.trim() || !effectiveSlug) {
      setError("Give the organization a name and a slug.");
      return;
    }
    setError(null);
    setPending("create");

    const { error: createError } = await authClient.organization.create({
      name: name.trim(),
      slug: effectiveSlug,
    });
    if (createError) {
      setError(
        createError.message ?? "The organization wasn't created. Check the slug and try again.",
      );
      setPending(null);
      return;
    }
    router.replace(destination);
  }

  async function choose(organizationId: string) {
    setError(null);
    setPending(organizationId);

    const { error: activeError } = await authClient.organization.setActive({ organizationId });
    if (activeError) {
      setError(activeError.message ?? "That organization couldn't be selected. Try again.");
      setPending(null);
      return;
    }
    router.replace(destination);
  }

  return (
    <div className="flex w-full max-w-sm flex-col gap-8">
      <div className="flex flex-col gap-2">
        <h1 className="text-2xl font-semibold tracking-tight text-balance">
          {memberships.length > 0 ? "Choose an organization" : "Create an organization"}
        </h1>
        <p className="text-muted-foreground">
          {hasActiveOrganization
            ? "Every project you link with the CLI belongs to an organization. A new one starts empty, with you as its owner."
            : "Every project you link with the CLI belongs to an organization, so the console needs one before it can show anything."}
        </p>
      </div>

      {memberships.length > 0 && (
        <div className="flex flex-col gap-2">
          <span className={labelType}>Your organizations</span>
          <ul className="border-t border-l border-border">
            {memberships.map((membership) => (
              <li key={membership.id} className="border-r border-b border-border">
                <button
                  type="button"
                  onClick={() => choose(membership.id)}
                  disabled={pending !== null}
                  className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left transition-colors outline-none hover:bg-muted focus-visible:bg-muted disabled:opacity-60"
                >
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-medium">{membership.name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {membership.slug}
                    </span>
                  </span>
                  <ArrowRightIcon className="size-4 shrink-0 text-muted-foreground" />
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}

      <form onSubmit={create} className="flex flex-col gap-5" noValidate>
        {memberships.length > 0 && <span className={labelType}>Or create a new one</span>}
        <label className="flex flex-col gap-2">
          <span className={labelType}>Name</span>
          <Input
            name="name"
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Acme"
            autoComplete="organization"
            autoFocus={memberships.length === 0}
            className="h-9"
          />
        </label>
        <label className="flex flex-col gap-2">
          <span className={labelType}>Slug</span>
          <Input
            name="slug"
            value={effectiveSlug}
            onChange={(event) => {
              setSlugEdited(true);
              setSlug(slugify(event.target.value));
            }}
            placeholder="acme"
            className="h-9"
            aria-describedby="slug-hint"
          />
          <span id="slug-hint" className="text-xs text-muted-foreground">
            Lowercase letters, numbers and dashes. The CLI shows it when you pick an organization.
          </span>
        </label>

        {error && (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        )}

        <div className="flex items-center gap-2">
          <Button type="submit" size="lg" disabled={pending !== null}>
            {pending === "create" ? "Creating…" : "Create organization"}
          </Button>
          {hasActiveOrganization && (
            <Button
              variant="ghost"
              size="lg"
              nativeButton={false}
              render={<Link href={destination} />}
            >
              Cancel
            </Button>
          )}
        </div>
      </form>
    </div>
  );
}
