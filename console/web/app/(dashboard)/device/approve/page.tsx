"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { AuthError, AuthPanel } from "@/components/auth-panel";
import { Button } from "@/components/ui/button";
import { authClient } from "@/lib/auth-client";

function formatForDisplay(code: string) {
  if (code.length === 8) return `${code.slice(0, 4)}-${code.slice(4)}`;
  return code;
}

function DeviceApprovalForm() {
  const searchParams = useSearchParams();
  const userCode = searchParams.get("user_code") ?? "";
  const [status, setStatus] = useState<
    "idle" | "approving" | "denying" | "approved" | "denied" | "error"
  >("idle");
  const [error, setError] = useState<string | null>(null);

  if (!userCode) {
    return (
      <StatusCard
        title="Missing code"
        message="No device code was provided. Go back to your terminal and re-run `ocel login`."
      />
    );
  }

  async function handleApprove() {
    setStatus("approving");
    setError(null);
    const { error: approveError } = await authClient.device.approve({
      userCode,
    });
    if (approveError) {
      setError(approveError.error_description ?? "Failed to approve device.");
      setStatus("error");
      return;
    }
    setStatus("approved");
  }

  async function handleDeny() {
    setStatus("denying");
    setError(null);
    const { error: denyError } = await authClient.device.deny({ userCode });
    if (denyError) {
      setError(denyError.error_description ?? "Failed to deny device.");
      setStatus("error");
      return;
    }
    setStatus("denied");
  }

  if (status === "approved") {
    return (
      <StatusCard
        title="Device approved"
        message="You're all set. You can close this window and return to your terminal."
      />
    );
  }

  if (status === "denied") {
    return (
      <StatusCard
        title="Device denied"
        message="You can close this window and return to your terminal."
      />
    );
  }

  const isBusy = status === "approving" || status === "denying";

  return (
    <AuthPanel
      title="Confirm device sign-in"
      description="Approve this sign-in for your Ocel account."
    >
      <div className="border border-border bg-muted py-4 text-center font-mono text-2xl tracking-[0.3em] text-foreground uppercase">
        {formatForDisplay(userCode)}
      </div>

      <p className="text-sm text-muted-foreground">
        A CLI device is requesting access to your Ocel account. If you didn&apos;t initiate this,
        deny it.
      </p>

      <div className="grid grid-cols-2 gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={handleDeny}
          disabled={isBusy}
          className="h-10"
        >
          {status === "denying" ? "Denying…" : "Deny"}
        </Button>
        <Button type="button" onClick={handleApprove} disabled={isBusy} className="h-10">
          {status === "approving" ? "Approving…" : "Approve"}
        </Button>
      </div>

      {error && <AuthError>{error}</AuthError>}
    </AuthPanel>
  );
}

function StatusCard({ title, message }: { title: string; message: string }) {
  return <AuthPanel title={title} description={message} />;
}

export default function DeviceApprovalPage() {
  return (
    <Suspense fallback={null}>
      <DeviceApprovalForm />
    </Suspense>
  );
}
