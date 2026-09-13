"use client";

import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { AuthError, AuthPanel } from "@/components/auth-panel";
import { Button } from "@/components/ui/button";
import { authClient } from "@/lib/auth-client";

function SignInForm() {
  const searchParams = useSearchParams();
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleGithubSignIn() {
    setError(null);
    setIsLoading(true);

    const redirect = searchParams.get("redirect");
    const callbackURL = redirect?.startsWith("/") ? redirect : "/";

    const { error: signInError } = await authClient.signIn.social({
      provider: "github",
      callbackURL,
    });

    if (signInError) {
      setError(signInError.message ?? "Failed to sign in with GitHub");
      setIsLoading(false);
    }
  }

  return (
    <AuthPanel title="Sign in" description="Use your GitHub account to continue">
      <Button
        type="button"
        onClick={handleGithubSignIn}
        disabled={isLoading}
        className="h-10 w-full gap-2"
      >
        <svg aria-hidden="true" viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
          <path d="M12 .5C5.65.5.5 5.65.5 12c0 5.09 3.29 9.4 7.86 10.93.57.1.78-.25.78-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.35-3.88-1.35-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.75 2.7 1.25 3.35.96.1-.75.4-1.25.73-1.54-2.55-.29-5.23-1.28-5.23-5.7 0-1.26.45-2.29 1.18-3.1-.12-.29-.51-1.46.11-3.04 0 0 .97-.31 3.18 1.18a11.05 11.05 0 0 1 5.79 0c2.21-1.49 3.18-1.18 3.18-1.18.62 1.58.23 2.75.11 3.04.74.81 1.18 1.84 1.18 3.1 0 4.43-2.69 5.41-5.25 5.7.41.36.78 1.06.78 2.15 0 1.55-.01 2.8-.02 3.18 0 .3.2.66.79.55A11.5 11.5 0 0 0 23.5 12c0-6.35-5.15-11.5-11.5-11.5Z" />
        </svg>
        {isLoading ? "Redirecting…" : "Sign in with GitHub"}
      </Button>

      {error && <AuthError>{error}</AuthError>}
    </AuthPanel>
  );
}

export default function SignInPage() {
  return (
    <Suspense fallback={null}>
      <SignInForm />
    </Suspense>
  );
}
