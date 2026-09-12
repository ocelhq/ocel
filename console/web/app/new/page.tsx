import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { listMemberships, resolveAccess } from "@/lib/access";
import { safeRedirect } from "@/lib/request-path";
import { NewOrganizationForm } from "./form";

export default async function NewOrganizationPage({
  searchParams,
}: {
  searchParams: Promise<{ redirect?: string }>;
}) {
  const destination = safeRedirect((await searchParams).redirect);
  const access = await resolveAccess(await headers());

  if (access.state === "signed-out") {
    const returnTo = `/new?redirect=${encodeURIComponent(destination)}`;
    redirect(`/sign-in?redirect=${encodeURIComponent(returnTo)}`);
  }

  const memberships =
    access.state === "no-organization" ? await listMemberships(access.userId) : [];

  return (
    <div className="flex min-h-full flex-1 flex-col">
      <header className="flex h-14 shrink-0 items-center border-b border-border px-5 md:px-8">
        <span className="font-display text-xl leading-none tracking-[-0.04em] lowercase">ocel</span>
      </header>
      <main className="flex flex-1 justify-center px-5 pt-16 pb-12 md:pt-24">
        <NewOrganizationForm
          destination={destination}
          memberships={memberships}
          hasActiveOrganization={access.state === "ready"}
        />
      </main>
    </div>
  );
}
