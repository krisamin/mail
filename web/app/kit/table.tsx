import type { ReactNode } from "react";

/** Data table for admin views — borders come from the wrapper Panel. */
export const Table = ({ children }: { children: ReactNode }) => (
  <div className="overflow-x-auto">
    <table className="w-full border-collapse text-sm">{children}</table>
  </div>
);

export const Th = ({ children, className = "" }: { children?: ReactNode; className?: string }) => (
  <th
    className={`border-b border-line px-4 py-2 text-left text-xs font-medium text-ink-3 ${className}`}
  >
    {children}
  </th>
);

export const Td = ({ children, className = "" }: { children?: ReactNode; className?: string }) => (
  <td className={`border-b border-line px-4 py-2.5 align-middle text-ink-2 ${className}`}>
    {children}
  </td>
);

export const Tr = ({ children, className = "" }: { children: ReactNode; className?: string }) => (
  <tr className={`last:[&>td]:border-0 hover:bg-raised/60 ${className}`}>{children}</tr>
);
