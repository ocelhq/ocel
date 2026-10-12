"use client";

import { useEffect } from "react";

const LIST = "#nd-sidebar [data-radix-scroll-area-viewport] > div";
const ITEM = "a[data-active], button[aria-expanded]";

export function follow(
  current: Pick<Element, "isConnected"> | null,
  visible: boolean,
): "place" | "hide" | "stay" {
  if (!visible) return "stay";
  return current?.isConnected ? "place" : "hide";
}

export function SidebarHover() {
  useEffect(() => {
    let host: HTMLElement | null = null;
    let marker: HTMLDivElement | null = null;
    let current: Element | null = null;
    const resize = new ResizeObserver(() => {
      const action = follow(current, marker?.dataset.visible === "true");
      if (action === "place" && current) place(current, false);
      if (action === "hide" && marker) {
        marker.dataset.visible = "false";
        current = null;
      }
    });

    function attach(list: HTMLElement) {
      if (host === list && marker?.isConnected) return;
      resize.disconnect();
      marker?.remove();
      host = list;
      host.style.position = "relative";
      marker = document.createElement("div");
      marker.className = "sidebar-hover";
      marker.setAttribute("aria-hidden", "true");
      host.append(marker);
      resize.observe(host);
    }

    function place(item: Element, glide: boolean) {
      if (!host || !marker) return;
      const from = host.getBoundingClientRect();
      const to = item.getBoundingClientRect();
      marker.dataset.gliding = String(glide);
      marker.style.translate = `${to.left - from.left}px ${to.top - from.top}px`;
      marker.style.width = `${to.width}px`;
      marker.style.height = `${to.height}px`;
      marker.dataset.visible = "true";
    }

    function over(event: PointerEvent) {
      const target = event.target as Element;
      const list = target.closest<HTMLElement>(LIST);
      const item = target.closest(ITEM);
      if (!list || !item || !list.contains(item)) return;
      attach(list);
      place(item, marker?.dataset.visible === "true");
      current = item;
    }

    function out(event: PointerEvent) {
      const next = event.relatedTarget as Element | null;
      if (host && marker && !host.contains(next)) {
        marker.dataset.visible = "false";
        current = null;
      }
    }

    document.addEventListener("pointerover", over);
    document.addEventListener("pointerout", out);
    return () => {
      document.removeEventListener("pointerover", over);
      document.removeEventListener("pointerout", out);
      resize.disconnect();
      marker?.remove();
    };
  }, []);

  return null;
}
