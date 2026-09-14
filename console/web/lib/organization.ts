import { administers } from "@console/auth";
import { db } from "@console/db";
import { invitation, member, organization, user } from "@console/db/schema";
import { and, asc, eq, gt } from "drizzle-orm";
import { type Role, roleNamed } from "./roles";

export type Organization = {
  id: string;
  name: string;
  slug: string;
  createdAt: Date;
  role: Role;
  administers: boolean;
};

export async function organizationOf(
  userId: string,
  organizationId: string,
): Promise<Organization | null> {
  const [row] = await db
    .select({
      id: organization.id,
      name: organization.name,
      slug: organization.slug,
      createdAt: organization.createdAt,
      role: member.role,
    })
    .from(member)
    .innerJoin(organization, eq(organization.id, member.organizationId))
    .where(and(eq(member.userId, userId), eq(member.organizationId, organizationId)))
    .limit(1);
  if (!row) {
    return null;
  }
  return { ...row, role: roleNamed(row.role), administers: administers(row.role) };
}

export type Member = {
  id: string;
  userId: string;
  name: string;
  email: string;
  image: string | null;
  role: Role;
  joinedAt: Date;
};

export async function membersOf(organizationId: string): Promise<Member[]> {
  const rows = await db
    .select({
      id: member.id,
      userId: member.userId,
      name: user.name,
      email: user.email,
      image: user.image,
      role: member.role,
      joinedAt: member.createdAt,
    })
    .from(member)
    .innerJoin(user, eq(user.id, member.userId))
    .where(eq(member.organizationId, organizationId))
    .orderBy(asc(member.createdAt));
  return rows.map((row) => ({ ...row, role: roleNamed(row.role) }));
}

export type Invitation = {
  id: string;
  email: string;
  role: Role;
  inviter: string;
  expiresAt: Date;
};

export async function invitationsOf(organizationId: string): Promise<Invitation[]> {
  const rows = await db
    .select({
      id: invitation.id,
      email: invitation.email,
      role: invitation.role,
      inviter: user.name,
      expiresAt: invitation.expiresAt,
    })
    .from(invitation)
    .innerJoin(user, eq(user.id, invitation.inviterId))
    .where(
      and(
        eq(invitation.organizationId, organizationId),
        eq(invitation.status, "pending"),
        gt(invitation.expiresAt, new Date()),
      ),
    )
    .orderBy(asc(invitation.createdAt));
  return rows.map((row) => ({ ...row, role: roleNamed(row.role ?? "member") }));
}

export type Invite = {
  id: string;
  email: string;
  role: Role;
  status: string;
  expiresAt: Date;
  organizationId: string;
  organizationName: string;
  inviter: string;
};

export async function inviteOf(id: string): Promise<Invite | null> {
  const [row] = await db
    .select({
      id: invitation.id,
      email: invitation.email,
      role: invitation.role,
      status: invitation.status,
      expiresAt: invitation.expiresAt,
      organizationId: invitation.organizationId,
      organizationName: organization.name,
      inviter: user.name,
    })
    .from(invitation)
    .innerJoin(organization, eq(organization.id, invitation.organizationId))
    .innerJoin(user, eq(user.id, invitation.inviterId))
    .where(eq(invitation.id, id))
    .limit(1);
  return row ? { ...row, role: roleNamed(row.role ?? "member") } : null;
}
