import type { ReactNode } from "react";

const toneMap = {
  ok: "bg-ok-soft text-ok",
  warn: "bg-warn-soft text-warn",
  bad: "bg-bad-soft text-bad",
  brand: "bg-brand-soft text-brand",
  muted: "bg-raised text-ink-3",
} as const;

export type BadgeTone = keyof typeof toneMap;

export const Badge = ({
  tone = "muted",
  children,
  className = "",
}: {
  tone?: BadgeTone;
  children: ReactNode;
  className?: string;
}) => (
  <span
    className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium ${toneMap[tone]} ${className}`}
  >
    {children}
  </span>
);

/** Count pill for sidebars — caps at 99+. */
export const CountBadge = ({ value, tone = "brand" }: { value: number; tone?: BadgeTone }) =>
  value > 0 ? (
    <Badge tone={tone} className="tabular-nums">
      {value > 99 ? "99+" : value}
    </Badge>
  ) : null;
