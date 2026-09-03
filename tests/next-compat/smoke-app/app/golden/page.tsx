export const revalidate = 3;

export default function Page() {
  return (
    <main>
      <p id="golden-body">golden-body:v1</p>
      <p>
        Rendered identically whether or not the edge asked for this page as a
        prefetch.
      </p>
    </main>
  );
}
