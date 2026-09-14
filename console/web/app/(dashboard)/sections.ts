import { type Environment, withEnvironment } from "@/lib/environment";

type ProjectPage = {
  section: string;
  label: string;
  heading?: string;
  empty: string;
  command?: string;
  aggregate?: string;
};

export const projectPages: ProjectPage[] = [
  {
    section: "deployments",
    label: "Deployments",
    empty:
      "Deploys run from your machine and land in your own cloud, so release history isn't reported to the console yet.",
    command: "ocel deployments ls",
  },
  {
    section: "variables",
    label: "Variables",
    empty:
      "Production and preview values live in your own cloud. Edit them from the terminal, or open the local editor.",
    command: "ocel env ui",
  },
  {
    section: "resources",
    label: "Resources",
    heading: "Declared in your app code",
    empty:
      "The overview draws what the last deploy landed. For every binding and grant, ask the terminal.",
    command: "ocel bindings ls",
  },
  {
    section: "domains",
    label: "Domains",
    heading: "Served from your own cloud",
    empty: "Check each hostname's DNS and certificate from the terminal.",
    command: "ocel domain ls",
  },
  {
    section: "monitoring",
    label: "Monitoring",
    heading: "Not in the console yet",
    empty:
      "Monitoring is planned. Until it lands, logs and metrics stay where your provider keeps them.",
  },
  {
    section: "spend",
    label: "Cloud spend",
    heading: "Not in the console yet",
    empty:
      "Spend is planned: what this project costs in your own cloud account, not what Ocel charges.",
    aggregate:
      "Spend is planned: what your projects cost in your own cloud account, not what Ocel charges.",
  },
];

export function projectPage(section: string) {
  return projectPages.find((page) => page.section === section);
}

export function sectionOf(pathname: string): string | null {
  const parts = pathname.split("/").filter(Boolean);
  if (parts.length === 0) {
    return "";
  }
  if (parts[0] === "projects" && parts[1]) {
    return parts[2] ?? "";
  }
  if (parts.length === 1 && projectPage(parts[0])) {
    return parts[0];
  }
  return null;
}

export function projectHref(slug: string, section: string, environment: Environment): string {
  return withEnvironment(`/projects/${slug}${section ? `/${section}` : ""}`, environment);
}

export function scopeHref(section: string | null): string {
  return section ? `/${section}` : "/";
}
