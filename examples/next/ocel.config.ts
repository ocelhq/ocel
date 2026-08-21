import { defineConfig } from "ocel/config";

export default defineConfig({
  slug: "next",
  apps: [
    {
      name: "next-app",
      framework: "next",
      path: ".",
      folder: "/next-app",
    },
  ],
});
