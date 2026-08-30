export default {
  slug: "express-container",
  provider: {
    package: "@ocel/provider-vps",
    options: { ssh: "my-box" },
  },
  apps: [
    {
      name: "web",
      path: ".",
      compute: "container",
      health: { path: "/healthz" },
    },
  ],
};
