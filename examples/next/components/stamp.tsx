export function Stamp({
  scope,
  cached,
  live,
}: {
  scope: string;
  cached: string | number;
  live: string | number;
}) {
  return (
    <div data-ocel-stamp={scope}>
      <span data-ocel={`${scope}:cached`}>{cached}</span>
      <span data-ocel={`${scope}:live`}>{live}</span>
    </div>
  );
}
