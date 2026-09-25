/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static export. The pages have no server, no API routes and no secrets:
  // everything they show is baked in at build time from data that is already
  // in this repository. A server would only create somewhere for runtime state
  // to leak to, and runtime state is exactly what must not leave the host.
  // The one function this deployment has, api/waitlist.js, lives outside
  // Next for that reason: it stores an email address and nothing else.
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
};
export default nextConfig;
