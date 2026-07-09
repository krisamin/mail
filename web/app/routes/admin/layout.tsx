import { Link, NavLink, Outlet, redirect } from "react-router";
import type { Route } from "./+types/layout";
import { getUser, isAdmin } from "~/lib/session.server";

// /admin/* 공통 가드: 로그인 안 됨 → /login, 그룹 없음 → 403.
// (진짜 방어선은 Go API의 JWT 검증 — 여기는 UX 레이어)
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  if (!user) {
    const url = new URL(request.url);
    throw redirect(`/login?returnTo=${encodeURIComponent(url.pathname)}`);
  }
  if (!isAdmin(user)) {
    throw new Response("관리자 권한이 필요해요", { status: 403 });
  }
  return { name: user.name, email: user.email };
};

const navItemList = [
  { to: "/admin", label: "대시보드", end: true },
  { to: "/admin/domain", label: "도메인" },
  { to: "/admin/account", label: "계정" },
  { to: "/admin/relay", label: "relay" },
  { to: "/admin/queue", label: "발송 큐" },
];

export default function AdminLayout({ loaderData }: Route.ComponentProps) {
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
                  end={item.end}
                  className={({ isActive }) =>
                    `rounded-md px-3 py-1.5 text-sm ${
                      isActive ? "bg-bg-3 text-text-0" : "text-text-2 hover:bg-bg-2 hover:text-text-1"
                    }`
                  }
                >
                  {item.label}
                </NavLink>
              ))}
            </nav>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-xs text-text-2">{loaderData.name}</span>
            <Link to="/logout" className="text-xs text-text-2 hover:text-text-1">
              로그아웃
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
