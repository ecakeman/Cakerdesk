import "./globals.css";
import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Cakerdesk",
  description: "面向长任务的 Agent 工作区",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
