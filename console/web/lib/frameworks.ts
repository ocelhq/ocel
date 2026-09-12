import type { Framework } from "@console/db/schema";
import {
  type SimpleIcon,
  siAstro,
  siBun,
  siDeno,
  siDjango,
  siExpress,
  siFastify,
  siGo,
  siHono,
  siNextdotjs,
  siNodedotjs,
  siNuxt,
  siPython,
  siReact,
  siRemix,
  siRust,
  siSvelte,
} from "simple-icons";

export type FrameworkLogo = { light: string; dark?: string };

export type FrameworkEntry = { label: string; icon: SimpleIcon; logo?: FrameworkLogo };

function logo(id: string, hasDark = false): FrameworkLogo {
  return {
    light: `/frameworks/${id}.svg`,
    dark: hasDark ? `/frameworks/${id}-dark.svg` : undefined,
  };
}

export const frameworkCatalog: Record<Framework, FrameworkEntry> = {
  nextjs: { label: "Next.js", icon: siNextdotjs, logo: logo("nextjs") },
  react: { label: "React", icon: siReact, logo: logo("react", true) },
  astro: { label: "Astro", icon: siAstro, logo: logo("astro", true) },
  remix: { label: "Remix", icon: siRemix, logo: logo("remix", true) },
  nuxt: { label: "Nuxt", icon: siNuxt, logo: logo("nuxt") },
  sveltekit: { label: "SvelteKit", icon: siSvelte, logo: logo("sveltekit") },
  node: { label: "Node.js", icon: siNodedotjs, logo: logo("node") },
  express: { label: "Express", icon: siExpress, logo: logo("express", true) },
  fastify: { label: "Fastify", icon: siFastify, logo: logo("fastify", true) },
  hono: { label: "Hono", icon: siHono, logo: logo("hono") },
  bun: { label: "Bun", icon: siBun, logo: logo("bun") },
  deno: { label: "Deno", icon: siDeno, logo: logo("deno", true) },
  go: { label: "Go", icon: siGo, logo: logo("go", true) },
  python: { label: "Python", icon: siPython, logo: logo("python") },
  django: { label: "Django", icon: siDjango, logo: logo("django") },
  rust: { label: "Rust", icon: siRust, logo: logo("rust", true) },
};
