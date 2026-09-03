export const revalidate = 5;

export const metadata = {
  openGraph: { url: "/hdr" },
};

export default function Page() {
  return <p id="hdr-token">hdr-token:{Date.now()}</p>;
}
