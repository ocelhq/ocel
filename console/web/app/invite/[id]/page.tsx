import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { AuthPanel } from "@/components/auth-panel";
import { getViewer, resolveAccess } from "@/lib/access";
import { inviteOf } from "@/lib/organization";
import { AcceptInvite } from "./accept";

export default async function InvitePage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const access = await resolveAccess(await headers());
  if (access.state === "signed-out") {
    redirect(`/sign-in?redirect=${encodeURIComponent(`/invite/${id}`)}`);
  }
  const userId = access.state === "ready" ? access.session.userId : access.userId;
  const [invite, viewer] = await Promise.all([inviteOf(id), getViewer(userId)]);

  if (invite?.status !== "pending" || invite.expiresAt < new Date()) {
    return (
      <AuthPanel
        title="This invitation is no longer open"
        description="It was used, cancelled or has expired. Ask whoever sent it for a new link."
      />
    );
  }

  return (
    <AcceptInvite
      invitationId={invite.id}
      organizationId={invite.organizationId}
      organizationName={invite.organizationName}
      inviter={invite.inviter}
      role={invite.role}
      email={invite.email}
      signedInAs={viewer?.email ?? ""}
    />
  );
}
