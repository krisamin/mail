import type { ReactNode } from "react";

/** Panel container — the standard bordered surface. */
export const Card = ({ className = "", children }: { className?: string; children: ReactNode }) => (
  <div className={`rounded-md border border-line bg-bg-1 ${className}`}>{children}</div>
);

/** Centered empty-state text inside a Card. */
export const EmptyText = ({ children }: { children: ReactNode }) => (
  <p className="px-4 py-6 text-center text-sm text-text-2">{children}</p>
);
