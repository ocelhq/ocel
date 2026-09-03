"use client";

export default function RouteError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <main>
      <h1>Something went wrong</h1>
      <p data-ocel="page">error</p>
      <p data-ocel="error:digest">{error.digest ?? "none"}</p>
      <button type="button" onClick={reset}>
        Try again
      </button>
    </main>
  );
}
