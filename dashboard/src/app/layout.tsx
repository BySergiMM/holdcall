import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Nim — Control Plane",
  description:
    "What Nim actually guarantees, what it does not, and what has not been tested. Built from the repository; refuses to build on an unsubstantiated claim.",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
