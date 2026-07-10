import { Link, NavLink, Outlet } from "react-router";
import type { Route } from "./+types/layout";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { isAdmin, requireUser } from "~/lib/session.server";

// Shared /admin/* guard: not signed in / expired token → /login, no group → 403.
// (The real defense is the Go API's JWT check — this layer is just UX.)
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  if (!isAdmin(user)) {
    const locale = await getLocale(request);
    throw new Response(translate(locale, "auth.adminRequired"), { status: 403 });
  }
  return { name: user.name, email: user.email };
};

const navItemList = [
  { to: "/admin", key: "nav.dashboard", end: true },
  { to: "/admin/domain", key: "nav.domain" },
  { to: "/admin/account", key: "nav.account" },
  { to: "/admin/relay", key: "nav.relay" },
  { to: "/admin/queue", key: "nav.queue" },
  { to: "/admin/system", key: "nav.system" },
] as const;

export default function AdminLayout({ loaderData }: Route.ComponentProps) {
  const t = useT();
  return (
    <div className="min-h-dvh">
      <header className="border-b border-line bg-bg-1">
        <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-3">
          <div className="flex items-center gap-6">
            <Link to="/" className="text-sm font-bold tracking-tight">
              mail <span className="text-accent">admin</span>
            </Link>
            <nav className="flex gap-1">
              {navItemList.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={"end" in item ? item.end : undefined}
                  className={({ isActive }) =>
                    `rounded-md px-3 py-1.5 text-sm ${
                      isActive ? "bg-bg-3 text-text-0" : "text-text-2 hover:bg-bg-2 hover:text-text-1"
                    }`
                  }
                >
                  {t(item.key)}
                </NavLink>
              ))}
            </nav>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-xs text-text-2">{loaderData.name}</span>
            <Link to="/logout" className="text-xs text-text-2 hover:text-text-1">
              {t("common.logout")}
            </Link>
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-4 py-6">
        <Outlet />
      </main>
    </div>
  );
}
