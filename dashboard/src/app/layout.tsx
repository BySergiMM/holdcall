import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Nim — the control that stays on your machine",
  description:
    "Nim sits between your agent and its MCP tools. It decides every call on your machine, shows you the real arguments before anything dangerous runs, and keeps a record you can verify offline.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
