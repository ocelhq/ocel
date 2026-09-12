"use client";

import { CaretUpDownIcon } from "@phosphor-icons/react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { type RunScope, runScopeOf, runScopes } from "@/lib/environment";

const labels: Record<RunScope, string> = {
  all: "All environments",
  production: "Production",
  preview: "Preview",
};

export function RunFilter() {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const scope = runScopeOf(searchParams.get("env"));

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={`Showing ${labels[scope].toLowerCase()}. Filter by environment`}
        className="flex h-8 shrink-0 items-center gap-1.5 border border-border bg-background px-2.5 text-sm outline-hidden transition-colors hover:bg-muted focus-visible:ring-1 focus-visible:ring-ring data-popup-open:bg-muted"
      >
        {labels[scope]}
        <CaretUpDownIcon className="size-4 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-44 p-1">
        <DropdownMenuRadioGroup
          value={scope}
          onValueChange={(next: RunScope) => {
            const params = new URLSearchParams(searchParams);
            params.delete("before");
            if (next === "all") {
              params.delete("env");
            } else {
              params.set("env", next);
            }
            const query = params.toString();
            router.push(query ? `${pathname}?${query}` : pathname);
          }}
        >
          {runScopes.map((item) => (
            <DropdownMenuRadioItem key={item} value={item} className="px-2.5 py-2 text-sm">
              {labels[item]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
