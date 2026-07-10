import type { ReactNode } from "react";

/** Action error feedback — renders nothing when message is empty. */
export const ErrorBanner = ({ message }: { message?: string | null }) =>
  message ? (
    <p role="alert" className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
      {message}
    </p>
  ) : null;

/** Informational banner (accent tone) with optional body. */
export const Banner = ({ title, children }: { title: ReactNode; children?: ReactNode }) => (
  <div className="rounded-md border border-accent/40 bg-accent-soft p-4">
    <p className="text-sm font-medium text-accent">{title}</p>
    {children}
  </div>
);
