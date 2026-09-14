"use client";

import { Menu as MenuPrimitive } from "@base-ui/react/menu";
import {
  ArrowBendUpLeftIcon,
  ArrowUpRightIcon,
  BookOpenIcon,
  BracketsCurlyIcon,
  BuildingsIcon,
  CaretUpDownIcon,
  CoinsIcon,
  DatabaseIcon,
  DotsThreeVerticalIcon,
  FoldersIcon,
  GithubLogoIcon,
  GlobeIcon,
  MonitorIcon,
  MoonIcon,
  PlugsIcon,
  PlusIcon,
  PulseIcon,
  RocketLaunchIcon,
  SignOutIcon,
  SquaresFourIcon,
  SunIcon,
  UsersIcon,
} from "@phosphor-icons/react";
import Link from "next/link";
import { useParams, usePathname, useRouter, useSearchParams } from "next/navigation";
import { useTheme } from "next-themes";
import { useRef, useState, useSyncExternalStore } from "react";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxSeparator,
  ComboboxTrigger,
} from "@/components/ui/combobox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarResizer,
} from "@/components/ui/sidebar";
import { authClient } from "@/lib/auth-client";
import { environmentOf } from "@/lib/environment";
import { projectHref, projectPages, scopeHref, sectionOf } from "./sections";

export type Viewer = { name: string; email: string; image: string | null };

export type OrganizationSummary = { id: string; name: string; slug: string };

const sectionIcons: Record<string, typeof SquaresFourIcon> = {
  "": SquaresFourIcon,
  deployments: RocketLaunchIcon,
  variables: BracketsCurlyIcon,
  resources: DatabaseIcon,
  domains: GlobeIcon,
  monitoring: PulseIcon,
  spend: CoinsIcon,
};

const scopedNavigation = [{ label: "Overview", section: "" }, ...projectPages].map(
  ({ label, section }) => ({ label, section, Icon: sectionIcons[section] ?? SquaresFourIcon }),
);

const organizationNavigation = [
  { label: "General", href: "/organization/general", Icon: BuildingsIcon },
  { label: "Members", href: "/organization/members", Icon: UsersIcon },
  { label: "Connectors", href: "/organization/connectors", Icon: PlugsIcon },
];

const themes = [
  { value: "system", label: "System theme", Icon: MonitorIcon },
  { value: "light", label: "Light theme", Icon: SunIcon },
  { value: "dark", label: "Dark theme", Icon: MoonIcon },
];

const menuItemClass = "gap-2.5 px-2.5 py-2.5 text-sm leading-5";

const searchInputClass =
  "m-0! h-10! border-0! bg-transparent! p-0 shadow-none! outline-none! has-[[data-slot=input-group-control]:focus-visible]:border-0! has-[[data-slot=input-group-control]:focus-visible]:ring-0! **:data-[slot=input-group-control]:h-10 **:data-[slot=input-group-control]:p-3 **:data-[slot=input-group-control]:text-sm **:data-[slot=input-group-control]:leading-5 **:data-[slot=input-group-control]:focus-visible:border-0! **:data-[slot=input-group-control]:focus-visible:ring-0!";

function initials(name: string) {
  return (
    name
      .split(/\s+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((word) => word[0]?.toUpperCase())
      .join("") || "?"
  );
}

function NavLink({
  label,
  href,
  active,
  Icon,
}: {
  label: string;
  href: string;
  active: boolean;
  Icon: typeof SquaresFourIcon;
}) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton render={<Link href={href} />} isActive={active} tooltip={label}>
        <Icon
          weight={active ? "fill" : "regular"}
          className={active ? undefined : "motion-safe:group-hover/menu-button:animate-wiggle"}
        />
        <span>{label}</span>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

function ScopedNavigation() {
  const pathname = usePathname();
  const params = useParams<{ slug?: string }>();
  const environment = environmentOf(useSearchParams().get("env"));
  const slug = pathname.startsWith("/projects/") ? params.slug : undefined;
  const current = sectionOf(pathname);

  return (
    <SidebarGroup
      key={slug ?? "all"}
      className="animate-in duration-200 fade-in-0 slide-in-from-left-1 motion-reduce:animate-none"
    >
      {slug && (
        <SidebarMenu className="mb-1">
          <SidebarMenuItem>
            <SidebarMenuButton
              render={<Link href="/" />}
              tooltip="All projects"
              className="text-muted-foreground"
            >
              <ArrowBendUpLeftIcon />
              <span>All projects</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      )}
      <SidebarGroupLabel className={slug ? "text-foreground" : undefined}>
        <span className="truncate">{slug ?? "All projects"}</span>
      </SidebarGroupLabel>
      <SidebarGroupContent>
        <SidebarMenu>
          {scopedNavigation.map(({ label, section, Icon }) => {
            const projects = !slug && section === "";
            return (
              <NavLink
                key={label}
                label={projects ? "Projects" : label}
                href={slug ? projectHref(slug, section, environment) : scopeHref(section)}
                active={current === section}
                Icon={projects ? FoldersIcon : Icon}
              />
            );
          })}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

function OrganizationNavigation() {
  const pathname = usePathname();
  return (
    <SidebarGroup className="mt-auto">
      <SidebarGroupLabel>Organization</SidebarGroupLabel>
      <SidebarGroupContent>
        <SidebarMenu>
          {organizationNavigation.map(({ label, href, Icon }) => (
            <NavLink key={label} label={label} href={href} active={pathname === href} Icon={Icon} />
          ))}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  );
}

function OrganizationSwitcher({
  organizations,
  activeOrganizationId,
}: {
  organizations: OrganizationSummary[];
  activeOrganizationId: string;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const anchorRef = useRef<HTMLDivElement>(null);
  const [pendingId, setPendingId] = useState<string | null>(null);

  const selectedId =
    pendingId && pendingId !== activeOrganizationId ? pendingId : activeOrganizationId;
  const active = organizations.find((item) => item.id === selectedId) ?? organizations[0];

  async function select(next: OrganizationSummary | null) {
    if (!next || next.id === activeOrganizationId) {
      return;
    }
    setPendingId(next.id);
    const { error } = await authClient.organization.setActive({ organizationId: next.id });
    if (error) {
      setPendingId(null);
      return;
    }
    if (pathname.startsWith("/projects/")) {
      router.push(scopeHref(sectionOf(pathname)));
    }
    router.refresh();
  }

  return (
    <Combobox
      items={organizations}
      value={active}
      onValueChange={select}
      itemToStringLabel={(item) => item.name}
    >
      <div ref={anchorRef} className="flex h-9 items-center gap-2 px-2">
        <span className="grid size-5 shrink-0 place-items-center bg-foreground text-[11px] font-semibold text-background">
          {initials(active.name)}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium">{active.name}</span>
        <ComboboxTrigger
          aria-label="Switch organization"
          className="grid size-7 shrink-0 place-items-center text-muted-foreground outline-hidden transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-sidebar-ring [&>svg:last-child]:hidden"
        >
          <CaretUpDownIcon className="size-4" />
        </ComboboxTrigger>
      </div>
      <ComboboxContent anchor={anchorRef} className="w-72">
        <ComboboxInput
          showTrigger={false}
          placeholder="Search organizations..."
          aria-label="Search organizations"
          className={searchInputClass}
        />
        <ComboboxSeparator />
        <ComboboxEmpty>No organizations found.</ComboboxEmpty>
        <ComboboxList>
          {(item: OrganizationSummary) => (
            <ComboboxItem key={item.id} value={item} className="text-sm leading-5">
              <span className="grid size-5 shrink-0 place-items-center bg-muted text-[11px] font-semibold text-foreground">
                {initials(item.name)}
              </span>
              <span className="truncate">{item.name}</span>
            </ComboboxItem>
          )}
        </ComboboxList>
        <ComboboxSeparator />
        <Link
          href="/new"
          className="flex w-full items-center gap-2 p-3 text-sm leading-5 outline-hidden hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent focus-visible:text-accent-foreground"
        >
          <PlusIcon className="size-4" />
          Create organization
        </Link>
      </ComboboxContent>
    </Combobox>
  );
}

function ViewerAvatar({ viewer, size }: { viewer: Viewer; size?: "sm" | "default" }) {
  return (
    <Avatar size={size} className="after:border-0">
      {viewer.image && <AvatarImage src={viewer.image} alt="" />}
      <AvatarFallback className="bg-foreground text-[11px] font-semibold text-background">
        {initials(viewer.name)}
      </AvatarFallback>
    </Avatar>
  );
}

function useMounted() {
  return useSyncExternalStore(
    () => () => {},
    () => true,
    () => false,
  );
}

function UserMenu({ viewer }: { viewer: Viewer }) {
  const router = useRouter();
  const { theme, setTheme } = useTheme();
  const mounted = useMounted();

  async function signOut() {
    await authClient.signOut();
    router.replace("/sign-in");
  }

  return (
    <div className="flex items-center gap-2">
      <span className="">
        <ViewerAvatar viewer={viewer} size="sm" />
      </span>
      <span className="flex min-w-0 flex-1 flex-col leading-tight">
        <span className="truncate font-medium">{viewer.name}</span>
        <span className="mt-0.5 truncate text-xs text-sidebar-foreground/60">{viewer.email}</span>
      </span>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label="Open account menu"
          className="grid size-7 shrink-0 place-items-center text-muted-foreground outline-hidden transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-sidebar-ring data-popup-open:bg-muted data-popup-open:text-foreground"
        >
          <DotsThreeVerticalIcon weight="bold" className="size-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align="end" sideOffset={8} className="w-72 p-1.5">
          <div className="flex items-center gap-3 px-2.5 py-3">
            <ViewerAvatar viewer={viewer} />
            <div className="flex min-w-0 flex-col">
              <span className="truncate text-sm font-medium text-foreground">{viewer.name}</span>
              <span className="truncate text-xs text-muted-foreground">{viewer.email}</span>
            </div>
          </div>
          <DropdownMenuSeparator className="my-1.5" />
          <div className="flex items-center justify-between gap-3 py-2 pr-2 pl-2.5 text-sm leading-5">
            <span>Theme</span>
            <MenuPrimitive.RadioGroup
              value={mounted ? (theme ?? "system") : undefined}
              onValueChange={(value) => setTheme(String(value))}
              className="flex border border-border"
            >
              {themes.map(({ value, label, Icon }) => (
                <MenuPrimitive.RadioItem
                  key={value}
                  value={value}
                  closeOnClick={false}
                  aria-label={label}
                  title={label}
                  className="grid size-7 place-items-center text-muted-foreground outline-hidden not-first:border-l not-first:border-border data-checked:bg-muted data-checked:text-foreground data-highlighted:bg-accent data-highlighted:text-foreground"
                >
                  <Icon className="size-3.5" />
                </MenuPrimitive.RadioItem>
              ))}
            </MenuPrimitive.RadioGroup>
          </div>
          <DropdownMenuSeparator className="my-1.5" />
          <DropdownMenuItem
            render={<a href="https://ocel.dev/docs" target="_blank" rel="noreferrer" />}
            className={menuItemClass}
          >
            <BookOpenIcon />
            Documentation
            <ArrowUpRightIcon className="ml-auto text-muted-foreground" />
          </DropdownMenuItem>
          <DropdownMenuItem
            render={<a href="https://github.com/ocelhq/ocel" target="_blank" rel="noreferrer" />}
            className={menuItemClass}
          >
            <GithubLogoIcon />
            GitHub
            <ArrowUpRightIcon className="ml-auto text-muted-foreground" />
          </DropdownMenuItem>
          <DropdownMenuSeparator className="my-1.5" />
          <DropdownMenuItem onClick={signOut} className={menuItemClass}>
            <SignOutIcon />
            Log out
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

export function AppSidebar({
  viewer,
  organizations,
  activeOrganizationId,
}: {
  viewer: Viewer;
  organizations: OrganizationSummary[];
  activeOrganizationId: string;
}) {
  return (
    <Sidebar collapsible="offcanvas">
      <SidebarHeader className="h-14 border-b border-sidebar-border">
        <OrganizationSwitcher
          organizations={organizations}
          activeOrganizationId={activeOrganizationId}
        />
      </SidebarHeader>
      <SidebarContent className="pt-2">
        <ScopedNavigation />
        <OrganizationNavigation />
      </SidebarContent>
      <SidebarFooter className="border-t border-sidebar-border p-3">
        <UserMenu viewer={viewer} />
      </SidebarFooter>
      <SidebarResizer />
    </Sidebar>
  );
}
