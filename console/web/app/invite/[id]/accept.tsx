"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { AuthError, AuthPanel } from "@/components/auth-panel";
import { Button } from "@/components/ui/button";
import { authClient } from "@/lib/auth-client";
import type { Role } from "@/lib/roles";

export function AcceptInvite({
  invitationId,
  organizationId,
  organizationName,
  inviter,
  role,
  email,
  signedInAs,
}: {
  invitationId: string;
  organizationId: string;
  organizationName: string;
  inviter: string;
  role: Role;
  email: string;
  signedInAs: string;
}) {
  const router = useRouter();
  const [pending, setPending] = useState<"accept" | "decline" | null>(null);
  const [declined, setDeclined] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const mismatch = signedInAs.toLowerCase() !== email.toLowerCase();

  async function accept() {
    setError(null);
    setPending("accept");
    const { error: acceptError } = await authClient.organization.acceptInvitation({
      invitationId,
    });
    if (acceptError) {
      setPending(null);
      setError(acceptError.message ?? "The invitation wasn’t accepted. Try again.");
      return;
    }
    await authClient.organization.setActive({ organizationId });
    router.replace("/");
    router.refresh();
  }

  async function decline() {
    setError(null);
    setPending("decline");
    const { error: declineError } = await authClient.organization.rejectInvitation({
      invitationId,
    });
    setPending(null);
    if (declineError) {
      setError(declineError.message ?? "The invitation is still open. Try again.");
      return;
    }
    setDeclined(true);
  }

  if (declined) {
    return (
      <AuthPanel
        title="Invitation declined"
        description={`${inviter} can send another if this was a mistake. You can close this window.`}
      />
    );
  }

  return (
    <AuthPanel
      title={`Join ${organizationName}`}
      description={`${inviter} invited you as ${role === "admin" ? "an admin" : `a ${role}`}. Members open every project in the organization.`}
    >
      {mismatch ? (
        <p className="text-sm text-muted-foreground">
          This invitation is for <span className="font-medium text-foreground">{email}</span>, and
          you are signed in as <span className="font-medium text-foreground">{signedInAs}</span>.
          Sign in with the invited address to accept it.
        </p>
      ) : (
        <div className="grid grid-cols-2 gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={decline}
            disabled={pending !== null}
            className="h-10"
          >
            {pending === "decline" ? "Declining…" : "Decline"}
          </Button>
          <Button type="button" onClick={accept} disabled={pending !== null} className="h-10">
            {pending === "accept" ? "Joining…" : "Accept"}
          </Button>
        </div>
      )}
      {error && <AuthError>{error}</AuthError>}
    </AuthPanel>
  );
}
