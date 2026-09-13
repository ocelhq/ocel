import type { Framework } from "@console/db/schema";
import type { ComponentProps, ComponentType } from "react";
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
import {
  AstroDarkIcon,
  AstroIcon,
  BunIcon,
  DenoDarkIcon,
  DenoIcon,
  DjangoIcon,
  ExpressDarkIcon,
  ExpressIcon,
  FastifyDarkIcon,
  FastifyIcon,
  GoDarkIcon,
  GoIcon,
  HonoIcon,
  NextjsIcon,
  NodeIcon,
  NuxtIcon,
  PythonIcon,
  ReactDarkIcon,
  ReactIcon,
  RemixDarkIcon,
  RemixIcon,
  RustDarkIcon,
  RustIcon,
  SvelteKitIcon,
} from "@/components/marks/frameworks";

export type BrandMark = ComponentType<ComponentProps<"svg">>;

export type FrameworkLogo = { light: BrandMark; dark?: BrandMark };

export type FrameworkEntry = { label: string; icon: SimpleIcon; logo?: FrameworkLogo };

export const frameworkCatalog: Record<Framework, FrameworkEntry> = {
  nextjs: { label: "Next.js", icon: siNextdotjs, logo: { light: NextjsIcon } },
  react: { label: "React", icon: siReact, logo: { light: ReactIcon, dark: ReactDarkIcon } },
  astro: { label: "Astro", icon: siAstro, logo: { light: AstroIcon, dark: AstroDarkIcon } },
  remix: { label: "Remix", icon: siRemix, logo: { light: RemixIcon, dark: RemixDarkIcon } },
  nuxt: { label: "Nuxt", icon: siNuxt, logo: { light: NuxtIcon } },
  sveltekit: { label: "SvelteKit", icon: siSvelte, logo: { light: SvelteKitIcon } },
  node: { label: "Node.js", icon: siNodedotjs, logo: { light: NodeIcon } },
  express: {
    label: "Express",
    icon: siExpress,
    logo: { light: ExpressIcon, dark: ExpressDarkIcon },
  },
  fastify: {
    label: "Fastify",
    icon: siFastify,
    logo: { light: FastifyIcon, dark: FastifyDarkIcon },
  },
  hono: { label: "Hono", icon: siHono, logo: { light: HonoIcon } },
  bun: { label: "Bun", icon: siBun, logo: { light: BunIcon } },
  deno: { label: "Deno", icon: siDeno, logo: { light: DenoIcon, dark: DenoDarkIcon } },
  go: { label: "Go", icon: siGo, logo: { light: GoIcon, dark: GoDarkIcon } },
  python: { label: "Python", icon: siPython, logo: { light: PythonIcon } },
  django: { label: "Django", icon: siDjango, logo: { light: DjangoIcon } },
  rust: { label: "Rust", icon: siRust, logo: { light: RustIcon, dark: RustDarkIcon } },
};
