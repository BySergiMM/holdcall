/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static export. The dashboard has no server, no API routes and no secrets:
  // everything it shows is baked in at build time from data that is already in
  // this repository. A server would only create somewhere for runtime state to
  // leak to, and runtime state is exactly what must not leave the host.
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
};
export default nextConfig;
