import type { ReactNode } from "react";

/** Bordered panel — the standard content surface. */
export const Panel = ({
  className = "",
  children,
}: {
  className?: string;
  children: ReactNode;
}) => (
  <div className={`overflow-hidden rounded-lg border border-line bg-surface ${className}`}>
    {children}
  </div>
);

/** Header row inside a Panel — title on the left, actions on the right. */
export const PanelHeader = ({
  title,
  description,
  action,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
}) => (
  <div className="flex items-center justify-between gap-4 border-b border-line px-4 py-3">
    <div className="min-w-0">
      <h2 className="truncate text-sm font-semibold text-ink">{title}</h2>
      {description && <p className="mt-0.5 text-xs text-ink-3">{description}</p>}
    </div>
    {action}
  </div>
);

/** Page heading — one per route, above the panels. */
export const PageHeader = ({
  title,
  description,
  action,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
}) => (
  <div className="flex items-start justify-between gap-4">
    <div className="min-w-0">
      <h1 className="text-lg font-semibold tracking-tight text-ink">{title}</h1>
      {description && <p className="mt-1 text-xs text-ink-3">{description}</p>}
    </div>
    {action}
  </div>
);

/** Empty state — icon, line, optional action. */
export const EmptyState = ({
  icon,
  title,
  description,
  action,
}: {
  icon?: ReactNode;
  title: string;
  description?: ReactNode;
  action?: ReactNode;
}) => (
  <div className="flex flex-col items-center justify-center gap-2 px-6 py-14 text-center">
    {icon && <div className="text-ink-faint">{icon}</div>}
    <p className="text-sm text-ink-2">{title}</p>
    {description && <p className="max-w-sm text-xs text-ink-3">{description}</p>}
    {action && <div className="mt-2">{action}</div>}
  </div>
);

/** Key/value row used by detail panels. */
export const DataRow = ({ label, children }: { label: string; children: ReactNode }) => (
  <div className="flex gap-3 py-1 text-xs">
    <span className="w-20 shrink-0 text-ink-3">{label}</span>
    <span className="min-w-0 flex-1 text-ink-2">{children}</span>
  </div>
);

/** One-line empty state inside a panel body. */
export const EmptyText = ({ children }: { children: ReactNode }) => (
  <p className="px-4 py-6 text-center text-sm text-ink-3">{children}</p>
);
