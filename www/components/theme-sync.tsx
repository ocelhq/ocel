"use client";

import { useLayoutEffect } from "react";

declare global {
  interface Window {
    __applyTheme?: () => void;
  }
}

// React rebuilds <html> from its own props when it recovers from a hydration mismatch (an
// extension touching the page is enough), dropping the theme the head script set. Applying it
// again before paint keeps the page from flashing to the light theme.
export function ThemeSync() {
  useLayoutEffect(() => {
    window.__applyTheme?.();
  }, []);
  return null;
}
