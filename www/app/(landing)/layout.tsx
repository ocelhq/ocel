import type { Metadata } from "next";
import localFont from "next/font/local";
import "./globals.css";
import { cn } from "@/lib/utils";

const spaceGrotesk = localFont({
  src: "../../node_modules/@ocelhq/theme/fonts/space-grotesk/space-grotesk-latin-300-700.woff2",
  weight: "300 700",
  variable: "--font-sans",
});
const ibmPlexMono = localFont({
  src: [
    {
      path: "../../node_modules/@ocelhq/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-400.woff2",
      weight: "400",
    },
    {
      path: "../../node_modules/@ocelhq/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-500.woff2",
      weight: "500",
    },
    {
      path: "../../node_modules/@ocelhq/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-600.woff2",
      weight: "600",
    },
  ],
  variable: "--font-mono",
});
const archivo = localFont({
  src: "../../node_modules/@ocelhq/theme/fonts/archivo/archivo-latin-800.woff2",
  weight: "800",
  variable: "--font-display",
});

const title = "Ocel - Deploy apps anywhere";
const description =
  "One CLI ships your app to AWS, Google Cloud, a VPS or Ocel Cloud. Your code declares the resources it needs, and Ocel builds them too.";

export const metadata: Metadata = {
  metadataBase: new URL("https://ocel.dev"),
  title,
  description,
  openGraph: { type: "website", siteName: "Ocel", url: "/", title, description },
  twitter: { card: "summary_large_image", title, description },
};

// The docs keep the reader's theme in localStorage under "theme" (fumadocs' next-themes,
// defaulting to the system's), so the landing reads the same key before it paints and
// follows the system while nothing is chosen.
const themeScript = `(() => {
  const root = document.documentElement;
  const media = matchMedia("(prefers-color-scheme: dark)");
  const apply = () => {
    let chosen = null;
    try { chosen = localStorage.getItem("theme"); } catch {}
    const dark = chosen === "dark" || ((!chosen || chosen === "system") && media.matches);
    root.classList.toggle("dark", dark);
    root.dataset.theme = dark ? "dark" : "light";
    root.style.colorScheme = dark ? "dark" : "light";
  };
  apply();
  media.addEventListener("change", apply);
})();`;

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      data-register="landing"
      suppressHydrationWarning
      className={cn(
        "h-full",
        "antialiased",
        "font-sans",
        spaceGrotesk.variable,
        ibmPlexMono.variable,
        archivo.variable,
      )}
    >
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body className="min-h-full flex flex-col">{children}</body>
    </html>
  );
}
