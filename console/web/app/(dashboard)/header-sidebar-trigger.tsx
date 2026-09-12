"use client";

import { SidebarTrigger, useSidebar } from "@/components/ui/sidebar";
import { cn } from "@/lib/utils";

export function HeaderSidebarTrigger() {
  const { state } = useSidebar();
  return (
    <SidebarTrigger
      className={cn(
        "-ml-1.5 shrink-0",
        state === "expanded" ? "md:hidden" : "animate-in duration-200 fade-in-0",
      )}
    />
  );
}
