import { NavLink } from "react-router";
import type { ReactNode } from "react";

// Secondary navigation — the column that sits between the rail and the
// content (mail folders, settings sections, admin sections).

export const SideNav = ({ children }: { children: ReactNode }) => (
  <nav className="flex flex-col gap-0.5 p-2">{children}</nav>
);

/** Titled group of items — admin uses these to stop being six flat tabs. */
export const SideNavGroup = ({ title, children }: { title: string; children: ReactNode }) => (
  <div className="mb-3 flex flex-col gap-0.5">
    <p className="px-2.5 pt-2 pb-1 text-[11px] font-medium tracking-wide text-ink-faint uppercase">
      {title}
    </p>
    {children}
  </div>
);

export const SideNavItem = ({
  to,
  icon,
  label,
  trailing,
  end,
}: {
  to: string;
  icon?: ReactNode;
  label: ReactNode;
  trailing?: ReactNode;
  end?: boolean;
}) => (
  <NavLink
    to={to}
    end={end}
    className={({ isActive }) =>
      `flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors duration-100 ${
        isActive
          ? "bg-raised font-medium text-ink"
          : "text-ink-2 hover:bg-raised/60 hover:text-ink"
      }`
    }
  >
    {icon && <span className="shrink-0 text-ink-3">{icon}</span>}
    <span className="min-w-0 flex-1 truncate">{label}</span>
    {trailing}
  </NavLink>
);
