import type { NextConfig } from "next";

// The dashboard is a static export: plain HTML, CSS and JS that `heall
// serve` hands out, so showing a run needs no Node process.
const nextConfig: NextConfig = {
  output: "export",
};

export default nextConfig;
