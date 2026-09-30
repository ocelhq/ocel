import localFont from "next/font/local";

const body = localFont({
  src: "../../../ui/theme/fonts/ibm-plex-sans/ibm-plex-sans-latin-100-700.woff2",
  weight: "400 600",
  variable: "--font-sans",
});

const heading = localFont({
  src: "../../../ui/theme/fonts/space-grotesk/space-grotesk-latin-300-700.woff2",
  weight: "500 700",
  variable: "--font-heading",
});

const mono = localFont({
  src: [
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-400.woff2", weight: "400" },
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-500.woff2", weight: "500" },
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-600.woff2", weight: "600" },
  ],
  variable: "--font-mono",
});

const display = localFont({
  src: "../../../ui/theme/fonts/archivo/archivo-latin-800.woff2",
  weight: "800",
  variable: "--font-display",
});

export const fontVariables = [body, heading, mono, display].map((font) => font.variable).join(" ");
