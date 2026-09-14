import { notFound } from "next/navigation";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { requireOrganization } from "@/lib/access";
import { invitationsOf, membersOf, organizationOf } from "@/lib/organization";
import { initials } from "@/lib/runs";
import { labelType } from "@/lib/type";
import { PageShell } from "../../page-shell";
import { Stamp } from "../../stamp";
import { InvitationActions, InviteForm, MemberActions, RoleCell } from "./actions";

const head = `h-9 px-5 ${labelType}`;
const cell = "h-14 px-5";

export default async function OrganizationMembersPage() {
  const session = await requireOrganization();
  const [held, members, invitations] = await Promise.all([
    organizationOf(session.userId, session.activeOrganizationId),
    membersOf(session.activeOrganizationId),
    invitationsOf(session.activeOrganizationId),
  ]);
  if (!held) {
    notFound();
  }
  const now = new Date().toISOString();
  const owners = members.filter((row) => row.role === "owner").length;

  return (
    <PageShell
      title="Members"
      description={
        held.administers
          ? "Everyone here can open every project in the organization."
          : "Everyone here can open every project in the organization. Owners and admins manage the list."
      }
    >
      {held.administers && <InviteForm organizationId={held.id} />}

      <div className="border border-border">
        <Table className="table-fixed text-sm/5">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className={head}>Member</TableHead>
              <TableHead className={`${head} w-32 md:w-40`}>Role</TableHead>
              <TableHead className={`${head} hidden w-36 md:table-cell`}>Joined</TableHead>
              <TableHead className={`${head} w-14`}>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {members.map((row) => {
              const self = row.userId === session.userId;
              return (
                <TableRow key={row.id} className="hover:bg-transparent">
                  <TableCell className={cell}>
                    <span className="flex min-w-0 items-center gap-3">
                      <Avatar className="after:border-0">
                        {row.image && <AvatarImage src={row.image} alt="" />}
                        <AvatarFallback className="bg-foreground text-[11px] font-semibold text-background">
                          {initials(row.name)}
                        </AvatarFallback>
                      </Avatar>
                      <span className="flex min-w-0 flex-col">
                        <span className="truncate font-medium">
                          {row.name}
                          {self && (
                            <span className="font-normal text-muted-foreground"> (you)</span>
                          )}
                        </span>
                        <span className="truncate text-xs text-muted-foreground">{row.email}</span>
                      </span>
                    </span>
                  </TableCell>
                  <TableCell className={cell}>
                    <RoleCell
                      organizationId={held.id}
                      memberId={row.id}
                      role={row.role}
                      editable={held.administers && !(self && row.role === "owner" && owners === 1)}
                      canGrantOwner={held.role === "owner"}
                    />
                  </TableCell>
                  <TableCell className={`${cell} hidden text-muted-foreground md:table-cell`}>
                    <Stamp at={row.joinedAt.toISOString()} now={now} />
                  </TableCell>
                  <TableCell className={`${cell} text-right`}>
                    {held.administers &&
                      !self &&
                      (row.role !== "owner" || held.role === "owner") && (
                        <MemberActions organizationId={held.id} memberId={row.id} name={row.name} />
                      )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

      {invitations.length > 0 && (
        <div className="flex flex-col gap-3">
          <h2 className={labelType}>Pending invitations</h2>
          <div className="border border-border">
            <Table className="table-fixed text-sm/5">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className={head}>Invited</TableHead>
                  <TableHead className={`${head} hidden w-40 md:table-cell`}>Role</TableHead>
                  <TableHead className={`${head} hidden w-36 md:table-cell`}>Expires</TableHead>
                  <TableHead className={`${head} w-28 md:w-56`}>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {invitations.map((row) => (
                  <TableRow key={row.id} className="hover:bg-transparent">
                    <TableCell className={cell}>
                      <span className="flex min-w-0 flex-col">
                        <span className="truncate font-medium">{row.email}</span>
                        <span className="truncate text-xs text-muted-foreground">
                          by {row.inviter}
                          <span className="md:hidden"> · {row.role}</span>
                        </span>
                      </span>
                    </TableCell>
                    <TableCell className={`${cell} hidden capitalize md:table-cell`}>
                      {row.role}
                    </TableCell>
                    <TableCell className={`${cell} hidden text-muted-foreground md:table-cell`}>
                      <Stamp at={row.expiresAt.toISOString()} now={now} />
                    </TableCell>
                    <TableCell className={`${cell} text-right`}>
                      {held.administers && <InvitationActions invitationId={row.id} />}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      )}
    </PageShell>
  );
}
