import type { ReactNode } from "react";

/** Headline number with a label — the admin dashboard is built from these. */
export const StatCard = ({
  label,
  value,
  tone = "text-ink",
  hint,
}: {
  label: string;
  value: ReactNode;
  tone?: string;
  hint?: ReactNode;
}) => (
  <div className="rounded-lg border border-line bg-surface px-4 py-3">
    <p className="text-xs text-ink-3">{label}</p>
    <p className={`mt-1 text-2xl font-semibold tabular-nums ${tone}`}>{value}</p>
    {hint && <p className="mt-0.5 text-xs text-ink-faint">{hint}</p>}
  </div>
);
