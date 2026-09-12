"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useState } from "react";
import { authClient } from "@/lib/auth-client";

function normalizeCode(raw: string) {
  return raw.trim().replace(/-/g, "").toUpperCase();
}

function DeviceVerificationForm() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const initialCode = searchParams.get("user_code") ?? "";
  const [userCode, setUserCode] = useState(initialCode);
  const [error, setError] = useState<string | null>(null);
  const [isChecking, setIsChecking] = useState(false);
  const [hasAutoAttempted, setHasAutoAttempted] = useState(false);

  const verifyAndContinue = useCallback(
    async (code: string) => {
      setError(null);
      setIsChecking(true);

      const formatted = normalizeCode(code);
      if (!formatted) {
        setError("Enter the code shown in your terminal.");
        setIsChecking(false);
        return;
      }

      const { error: verifyError } = await authClient.device({
        query: { user_code: formatted },
      });

      if (verifyError) {
        setError(
          verifyError.error_description ??
            "That code is invalid or has expired. Double-check it and try again.",
        );
        setIsChecking(false);
        return;
      }

      router.push(`/device/approve?user_code=${encodeURIComponent(formatted)}`);
    },
    [router],
  );

  useEffect(() => {
    if (initialCode && !hasAutoAttempted) {
      setHasAutoAttempted(true);
      verifyAndContinue(initialCode);
    }
  }, [initialCode, hasAutoAttempted, verifyAndContinue]);

  return (
    <div className="flex flex-1 items-center justify-center bg-zinc-50 font-sans dark:bg-black">
      <div className="flex w-full max-w-sm flex-col gap-6 rounded-2xl border border-black/[.08] bg-white p-8 dark:border-white/[.145] dark:bg-zinc-950">
        <div className="flex flex-col gap-2 text-center">
          <h1 className="text-2xl font-semibold tracking-tight text-black dark:text-zinc-50">
            Device authorization
          </h1>
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            Enter the code shown in your terminal to continue.
          </p>
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            verifyAndContinue(userCode);
          }}
          className="flex flex-col gap-4"
        >
          <input
            type="text"
            value={userCode}
            onChange={(e) => setUserCode(e.target.value)}
            placeholder="XXXX-XXXX"
            maxLength={12}
            disabled={isChecking}
            className="h-12 w-full rounded-lg border border-black/[.08] bg-transparent px-4 text-center font-mono text-lg uppercase tracking-widest text-black outline-none focus:border-black/30 disabled:opacity-60 dark:border-white/[.145] dark:text-zinc-50 dark:focus:border-white/40"
          />

          <button
            type="submit"
            disabled={isChecking}
            className="flex h-11 w-full items-center justify-center gap-2 rounded-full bg-foreground px-5 text-sm font-medium text-background transition-colors hover:bg-[#383838] disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-[#ccc]"
          >
            {isChecking ? "Checking…" : "Continue"}
          </button>
        </form>

        {error && <p className="text-center text-sm text-red-600 dark:text-red-400">{error}</p>}
      </div>
    </div>
  );
}

export default function DeviceVerificationPage() {
  return (
    <Suspense fallback={null}>
      <DeviceVerificationForm />
    </Suspense>
  );
}
