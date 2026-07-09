import type { ReactNode } from "react";

/** Page heading with optional description and right-aligned aside. */
export const PageTitle = ({
  title,
  description,
  aside,
}: {
  title: string;
  description?: ReactNode;
  aside?: ReactNode;
}) => (
  <div className="flex items-start justify-between gap-4">
    <div>
      <h1 className="text-xl font-bold">{title}</h1>
      {description && <p className="mt-0.5 text-xs text-text-2">{description}</p>}
    </div>
    {aside}
  </div>
);
