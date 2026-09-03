"use client";

export default function Error({ reset }: { error: Error; reset: () => void }) {
  return (
    <main>
      <h1>Something went wrong</h1>
      <p data-ocel="page">error</p>
      <button type="button" onClick={reset}>
        Try again
      </button>
    </main>
  );
}
