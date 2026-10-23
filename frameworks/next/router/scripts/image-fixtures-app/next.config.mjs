export default {
  images: {
    remotePatterns: [{ protocol: "https", hostname: "**.allowed.example", pathname: "/img/**" }],
    domains: ["legacy.example"],
    dangerouslyAllowSVG: process.env.OCEL_IMAGE_FIXTURES === "svg",
  },
};
