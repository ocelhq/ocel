"use client";

import { env } from "../infra/env";

export function BrowserGreeting() {
  return <p data-browser-value={env.NEXT_PUBLIC_GREETING}>read in the browser</p>;
}
