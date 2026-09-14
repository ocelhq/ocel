"use client";

import { CaretUpDownIcon, CheckIcon, LinkSimpleIcon, XIcon } from "@phosphor-icons/react";
import { useRouter } from "next/navigation";
import { type FormEvent, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import { authClient } from "@/lib/auth-client";
import { type Role, roles } from "@/lib/roles";
import { labelType } from "@/lib/type";

const picker =
  "flex h-8 items-center gap-1.5 border border-border bg-background px-2.5 text-sm capitalize outline-hidden transition-colors hover:bg-muted focus-visible:ring-1 focus-visible:ring-ring data-popup-open:bg-muted disabled:pointer-events-none disabled:opacity-50";

function inviteHref(id: string) {
  return `${window.location.origin}/invite/${id}`;
}

function useCopy() {
  const [copied, setCopied] = useState(false);
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }
  return { copied, copy };
}

function RolePicker({
  value,
  onChange,
  options,
  disabled,
  label,
}: {
  value: Role;
  onChange: (next: Role) => void;
  options: readonly Role[];
  disabled?: boolean;
  label: string;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={label} disabled={disabled} className={picker}>
        {value}
        <CaretUpDownIcon className="size-4 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-40 p-1">
        <DropdownMenuRadioGroup value={value} onValueChange={(next) => onChange(next as Role)}>
          {options.map((role) => (
            <DropdownMenuRadioItem
              key={role}
              value={role}
              className="px-2.5 py-2 text-sm capitalize"
            >
              {role}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function InviteForm({ organizationId }: { organizationId: string }) {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<{ id: string; email: string } | null>(null);
  const { copied, copy } = useCopy();

  async function invite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const address = email.trim().toLowerCase();
    if (!address) {
      setError("Enter an email address.");
      return;
    }
    setError(null);
    setPending(true);
    const { data, error: inviteError } = await authClient.organization.inviteMember({
      organizationId,
      email: address,
      role,
      resend: true,
    });
    setPending(false);
    if (inviteError || !data) {
      setError(inviteError?.message ?? "The invitation wasn’t created. Try again.");
      return;
    }
    setCreated({ id: data.id, email: address });
    setEmail("");
    router.refresh();
  }

  return (
    <div className="flex max-w-2xl flex-col gap-4">
      <form onSubmit={invite} className="flex flex-col gap-3" noValidate>
        <span className={labelType}>Invite someone</span>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            type="email"
            name="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder="teammate@example.com"
            aria-label="Email address"
            autoComplete="off"
            disabled={pending}
            className="min-w-56 flex-1"
          />
          <RolePicker
            value={role}
            onChange={setRole}
            options={["member", "admin"]}
            disabled={pending}
            label={`Role: ${role}. Change role`}
          />
          <Button type="submit" disabled={pending}>
            {pending ? "Inviting…" : "Invite"}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          The console sends no email. Copy the link and send it yourself; it works only for someone
          signed in with that address, and expires in two days.
        </p>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </form>
      {created && (
        <div
          role="status"
          className="flex flex-wrap items-center gap-x-4 gap-y-2 border border-border px-4 py-3 text-sm"
        >
          <span className="min-w-0 flex-1">
            Invitation for <span className="font-medium">{created.email}</span> is ready.
          </span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => copy(inviteHref(created.id))}
          >
            {copied ? (
              <CheckIcon data-icon="inline-start" className="text-go" />
            ) : (
              <LinkSimpleIcon data-icon="inline-start" />
            )}
            {copied ? "Copied" : "Copy link"}
          </Button>
        </div>
      )}
    </div>
  );
}

export function RoleCell({
  organizationId,
  memberId,
  role,
  editable,
  canGrantOwner,
}: {
  organizationId: string;
  memberId: string;
  role: Role;
  editable: boolean;
  canGrantOwner: boolean;
}) {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!editable) {
    return <span className="capitalize">{role}</span>;
  }

  async function change(next: Role) {
    if (next === role) {
      return;
    }
    setError(null);
    setPending(true);
    const { error: roleError } = await authClient.organization.updateMemberRole({
      organizationId,
      memberId,
      role: next,
    });
    setPending(false);
    if (roleError) {
      setError(roleError.message ?? "The role wasn’t changed.");
      return;
    }
    router.refresh();
  }

  return (
    <span className="flex flex-col items-start gap-1">
      <RolePicker
        value={role}
        onChange={change}
        options={canGrantOwner ? roles : ["member", "admin"]}
        disabled={pending}
        label={`Role: ${role}. Change role`}
      />
      {error && (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      )}
    </span>
  );
}

export function MemberActions({
  organizationId,
  memberId,
  name,
}: {
  organizationId: string;
  memberId: string;
  name: string;
}) {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function remove() {
    setError(null);
    setPending(true);
    const { error: removeError } = await authClient.organization.removeMember({
      organizationId,
      memberIdOrEmail: memberId,
    });
    setPending(false);
    if (removeError) {
      setError(removeError.message ?? "They’re still a member.");
      return;
    }
    router.refresh();
  }

  return (
    <Popover>
      <PopoverTrigger
        render={<Button variant="ghost" size="icon-sm" aria-label={`Remove ${name}`} />}
      >
        <XIcon />
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 gap-3 p-4">
        <PopoverHeader>
          <PopoverTitle>Remove {name}?</PopoverTitle>
          <PopoverDescription>
            They lose access to every project here. Nothing they deployed is touched, and an owner
            can invite them back.
          </PopoverDescription>
        </PopoverHeader>
        <div className="flex items-center gap-2">
          <Button type="button" variant="destructive" size="sm" disabled={pending} onClick={remove}>
            {pending ? "Removing…" : "Remove"}
          </Button>
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
      </PopoverContent>
    </Popover>
  );
}

export function InvitationActions({ invitationId }: { invitationId: string }) {
  const router = useRouter();
  const { copied, copy } = useCopy();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function cancel() {
    setError(null);
    setPending(true);
    const { error: cancelError } = await authClient.organization.cancelInvitation({
      invitationId,
    });
    setPending(false);
    if (cancelError) {
      setError(cancelError.message ?? "The invitation is still open.");
      return;
    }
    router.refresh();
  }

  return (
    <span className="inline-flex flex-col items-end gap-1">
      <span className="inline-flex flex-wrap items-center justify-end gap-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => copy(inviteHref(invitationId))}
        >
          {copied ? (
            <CheckIcon data-icon="inline-start" className="text-go" />
          ) : (
            <LinkSimpleIcon data-icon="inline-start" />
          )}
          {copied ? "Copied" : "Copy link"}
        </Button>
        <Button type="button" variant="ghost" size="sm" disabled={pending} onClick={cancel}>
          {pending ? "Cancelling…" : "Cancel"}
        </Button>
      </span>
      {error && (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      )}
    </span>
  );
}
