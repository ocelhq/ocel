"use client";

import { useEffect, useState } from "react";
import { env } from "../../src/env";

function attempt(read: () => unknown): string {
  try {
    const value = read();
    return `ok ${typeof value} ${JSON.stringify(value)}`;
  } catch (error) {
    return `threw ${(error as Error).name}: ${(error as Error).message}`;
  }
}

export function Probe() {
  const [result, setResult] = useState("pending");
  useEffect(() => {
    setResult(
      JSON.stringify({
        api: attempt(() => env.NEXT_PUBLIC_API_URL),
        retries: attempt(() => env.NEXT_PUBLIC_RETRIES),
        stripe: attempt(() => env.STRIPE_KEY),
      }),
    );
  }, []);
  return <pre id="client">{result}</pre>;
}
