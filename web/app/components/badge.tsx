import type { ReactNode } from "react";

// Status badge — tiny colored pill for states (active, DKIM, queue status, ...).
const tones = {
  ok: "bg-ok/20 text-ok",
  warn: "bg-warn/20 text-warn",
  bad: "bg-bad/20 text-bad",
  accent: "bg-accent-soft text-accent",
  muted: "bg-bg-3 text-muted",
} as const;

export type BadgeTone = keyof typeof tones;

export const Badge = ({
  tone = "muted",
  className = "",
  children,
}: {
  tone?: BadgeTone;
  className?: string;
  children: ReactNode;
}) => (
  <span className={`rounded px-1.5 py-0.5 text-[10px] ${tones[tone]} ${className}`}>
    {children}
  </span>
);
