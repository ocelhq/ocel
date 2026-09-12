import type { Framework } from "@console/db/schema";
import { CubeIcon } from "@phosphor-icons/react/dist/ssr";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { type FrameworkEntry, frameworkCatalog } from "@/lib/frameworks";

const VISIBLE = 3;

const tile = "bg-background not-first:-ml-px";

function FrameworkAvatar({ entry }: { entry: FrameworkEntry }) {
  return (
    <Avatar title={entry.label} aria-hidden className={tile}>
      {entry.logo && (
        <AvatarImage
          src={entry.logo.light}
          alt=""
          className={`m-auto size-4 object-contain ${entry.logo.dark ? "dark:hidden" : ""}`}
        />
      )}
      {entry.logo?.dark && (
        <AvatarImage
          src={entry.logo.dark}
          alt=""
          className="m-auto hidden size-4 object-contain dark:block"
        />
      )}
      <AvatarFallback className="bg-background">
        <svg viewBox="0 0 24 24" aria-hidden="true" className="size-4" fill={`#${entry.icon.hex}`}>
          <path d={entry.icon.path} />
        </svg>
      </AvatarFallback>
    </Avatar>
  );
}

export function FrameworkStack({ frameworks }: { frameworks: Framework[] }) {
  if (frameworks.length === 0) {
    return (
      <Avatar title="Frameworks not detected yet" className="after:border-dashed after:border-dim">
        <AvatarFallback className="bg-background text-dim">
          <CubeIcon aria-hidden className="size-4" />
          <span className="sr-only">Frameworks not detected yet</span>
        </AvatarFallback>
      </Avatar>
    );
  }

  const shown = frameworks.slice(0, VISIBLE);
  const hidden = frameworks.slice(VISIBLE);

  return (
    <span className="flex w-fit">
      {shown.map((id) => (
        <FrameworkAvatar key={id} entry={frameworkCatalog[id]} />
      ))}
      {hidden.length > 0 && (
        <Avatar
          aria-hidden
          title={hidden.map((id) => frameworkCatalog[id].label).join(", ")}
          className={tile}
        >
          <AvatarFallback className="bg-background font-mono text-[11px] font-medium">
            +{hidden.length}
          </AvatarFallback>
        </Avatar>
      )}
      <span className="sr-only">
        {frameworks.map((id) => frameworkCatalog[id].label).join(", ")}
      </span>
    </span>
  );
}
