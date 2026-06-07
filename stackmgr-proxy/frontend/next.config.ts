import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "export",
  basePath: "",
  unoptimizedImages: true,
  typescript: {
    tsconfigPath: "./tsconfig.json",
  },
};

export default nextConfig;
