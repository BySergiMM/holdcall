import type { Metadata } from "next";
import "./globals.css";

const description =
  "Holdcall sits between your agent and its MCP tools. It decides every call on your machine, shows you the real arguments before anything dangerous runs, and keeps a record you can verify offline.";

export const metadata: Metadata = {
  metadataBase: new URL("https://holdcall.vercel.app"),
  title: "Holdcall — the control that stays on your machine",
  description,
  alternates: { canonical: "/" },
  openGraph: {
    type: "website",
    siteName: "Holdcall",
    url: "/",
    title: "Holdcall — the control that stays on your machine",
    description,
    images: [{ url: "/og.png", width: 1200, height: 630, alt: "Holdcall: every MCP tool call decided and recorded on your machine" }],
  },
  twitter: {
    card: "summary_large_image",
    title: "Holdcall — the control that stays on your machine",
    description,
    images: ["/og.png"],
  },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
