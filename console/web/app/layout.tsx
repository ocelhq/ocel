import type { Metadata } from "next";
import { ThemeProvider } from "next-themes";
import "./globals.css";
import { TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { fontVariables } from "./fonts";

export const metadata: Metadata = {
  title: "Ocel Console",
  description: "Manage Ocel deployments in your cloud account.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      data-register="operate"
      suppressHydrationWarning
      className={cn("h-full", "antialiased", "font-sans", fontVariables)}
    >
      <body className="min-h-full flex flex-col text-sm/5">
        <ThemeProvider
          attribute="class"
          defaultTheme="system"
          enableSystem
          disableTransitionOnChange
        >
          <TooltipProvider>{children}</TooltipProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
