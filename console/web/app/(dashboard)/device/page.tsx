"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useState } from "react";
import { AuthError, AuthPanel } from "@/components/auth-panel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
    <AuthPanel
      title="Device authorization"
      description="Enter the code shown in your terminal to continue."
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          verifyAndContinue(userCode);
        }}
        className="flex flex-col gap-3"
      >
        <Input
          type="text"
          value={userCode}
          onChange={(e) => setUserCode(e.target.value)}
          placeholder="XXXX-XXXX"
          maxLength={12}
          disabled={isChecking}
          aria-label="Device code"
          aria-invalid={error ? true : undefined}
          autoComplete="off"
          spellCheck={false}
          className="h-12 text-center font-mono text-lg tracking-[0.3em] uppercase placeholder:tracking-[0.3em]"
        />

        <Button type="submit" disabled={isChecking} className="h-10 w-full">
          {isChecking ? "Checking…" : "Continue"}
        </Button>
      </form>

      {error && <AuthError>{error}</AuthError>}
    </AuthPanel>
  );
}

export default function DeviceVerificationPage() {
  return (
    <Suspense fallback={null}>
      <DeviceVerificationForm />
    </Suspense>
  );
}
