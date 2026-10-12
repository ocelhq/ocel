export function Cold({ n }: { n: string }) {
  return (
    <main>
      <h1>cold {n}</h1>
      <span data-ocel="cold:static">prerendered at build</span>
    </main>
  );
}
