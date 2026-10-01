import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "heall",
  description: "Live view of a heall run: bisect, heal attempts and guardrails",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className="h-full antialiased">
      <body className="min-h-full">{children}</body>
    </html>
  );
}
