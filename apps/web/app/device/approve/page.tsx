"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { authClient } from "@/lib/auth-client";

function formatForDisplay(code: string) {
  if (code.length === 8) return `${code.slice(0, 4)}-${code.slice(4)}`;
  return code;
}

function DeviceApprovalForm() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { data: session, isPending: isSessionPending } =
    authClient.useSession();

  const userCode = searchParams.get("user_code") ?? "";
  const [status, setStatus] = useState<
    "idle" | "approving" | "denying" | "approved" | "denied" | "error"
  >("idle");
  const [error, setError] = useState<string | null>(null);

  if (isSessionPending) return null;

  if (!session?.user) {
    const verificationPath = `/device?user_code=${encodeURIComponent(userCode)}`;
    router.replace(`/sign-in?redirect=${encodeURIComponent(verificationPath)}`);
    return null;
  }

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
    <div className="flex flex-1 items-center justify-center bg-zinc-50 font-sans dark:bg-black">
      <div className="flex w-full max-w-sm flex-col gap-6 rounded-2xl border border-black/[.08] bg-white p-8 dark:border-white/[.145] dark:bg-zinc-950">
        <div className="flex flex-col gap-2 text-center">
          <h1 className="text-2xl font-semibold tracking-tight text-black dark:text-zinc-50">
            Confirm device sign-in
          </h1>
          <p className="text-sm text-zinc-600 dark:text-zinc-400">
            Signed in as{" "}
            <span className="font-medium text-black dark:text-zinc-50">
              {session.user.email}
            </span>
          </p>
        </div>

        <div className="rounded-lg border border-black/[.08] bg-zinc-50 py-3 text-center font-mono text-lg uppercase tracking-widest text-black dark:border-white/[.145] dark:bg-zinc-900 dark:text-zinc-50">
          {formatForDisplay(userCode)}
        </div>

        <p className="text-center text-sm text-zinc-600 dark:text-zinc-400">
          A CLI device is requesting access to your Ocel account. If you
          didn&apos;t initiate this, deny it.
        </p>

        <div className="flex gap-3">
          <button
            type="button"
            onClick={handleDeny}
            disabled={isBusy}
            className="flex h-11 w-full items-center justify-center rounded-full border border-black/[.08] px-5 text-sm font-medium text-black transition-colors hover:bg-black/[.03] disabled:cursor-not-allowed disabled:opacity-60 dark:border-white/[.145] dark:text-zinc-50 dark:hover:bg-white/[.06]"
          >
            {status === "denying" ? "Denying…" : "Deny"}
          </button>
          <button
            type="button"
            onClick={handleApprove}
            disabled={isBusy}
            className="flex h-11 w-full items-center justify-center rounded-full bg-foreground px-5 text-sm font-medium text-background transition-colors hover:bg-[#383838] disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-[#ccc]"
          >
            {status === "approving" ? "Approving…" : "Approve"}
          </button>
        </div>

        {error && (
          <p className="text-center text-sm text-red-600 dark:text-red-400">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

function StatusCard({ title, message }: { title: string; message: string }) {
  return (
    <div className="flex flex-1 items-center justify-center bg-zinc-50 font-sans dark:bg-black">
      <div className="flex w-full max-w-sm flex-col gap-2 rounded-2xl border border-black/[.08] bg-white p-8 text-center dark:border-white/[.145] dark:bg-zinc-950">
        <h1 className="text-2xl font-semibold tracking-tight text-black dark:text-zinc-50">
          {title}
        </h1>
        <p className="text-sm text-zinc-600 dark:text-zinc-400">{message}</p>
      </div>
    </div>
  );
}

export default function DeviceApprovalPage() {
  return (
    <Suspense fallback={null}>
      <DeviceApprovalForm />
    </Suspense>
  );
}
