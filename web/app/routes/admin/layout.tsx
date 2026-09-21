import { Outlet } from "react-router";
import type { Route } from "./+types/layout";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { readPreference, themeFromCookie } from "~/lib/preference.server";
import { isAdmin, requireUser } from "~/lib/session.server";
import { AppShell } from "~/shell/app-shell";
import { SideNav, SideNavGroup, SideNavItem } from "~/shell/side-nav";
import {
  ActivityIcon,
  AddressIcon,
  DashboardIcon,
  DomainIcon,
  QueueIcon,
  RelayIcon,
  ShieldIcon,
  UserIcon,
} from "~/kit";

// Admin area. The sections used to be six flat tabs; they are grouped now by
// what the operator is actually doing — who gets mail, how mail leaves, how
// the server is doing.
//
// The real authorisation is the Go API's JWT + group check; this guard is UX
// (and RR runs parent/child loaders in parallel, so every child guards too).
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  if (!isAdmin(user)) {
    throw new Response(translate(await getLocale(request), "auth.adminRequired"), { status: 403 });
  }
  const preference = await readPreference(user);
  return {
    user: {
      name: user.name,
      email: user.email,
      admin: true,
      theme: preference.theme === "system" ? themeFromCookie(request) : preference.theme,
    },
  };
};

export default function AdminLayout({ loaderData }: Route.ComponentProps) {
  const t = useT();
  const sidebar = (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-4 pt-4 pb-1">
        <p className="text-sm font-semibold text-ink">{t("admin.title")}</p>
      </div>
      <SideNav>
        <SideNavItem to="/admin" end icon={<DashboardIcon className="size-4" />} label={t("admin.dashboard")} />
        <SideNavGroup title={t("admin.groupMail")}>
          <SideNavItem to="/admin/domain" icon={<DomainIcon className="size-4" />} label={t("admin.domain")} />
          <SideNavItem to="/admin/account" icon={<UserIcon className="size-4" />} label={t("admin.account")} />
          <SideNavItem to="/admin/group" icon={<ShieldIcon className="size-4" />} label={t("admin.group")} />
        </SideNavGroup>
        <SideNavGroup title={t("admin.groupDelivery")}>
          <SideNavItem to="/admin/relay" icon={<RelayIcon className="size-4" />} label={t("admin.relay")} />
          <SideNavItem to="/admin/queue" icon={<QueueIcon className="size-4" />} label={t("admin.queue")} />
        </SideNavGroup>
        <SideNavGroup title={t("admin.groupServer")}>
          <SideNavItem to="/admin/system" icon={<ActivityIcon className="size-4" />} label={t("admin.system")} />
        </SideNavGroup>
      </SideNav>
    </div>
  );
  return (
    <AppShell user={loaderData.user} sidebar={sidebar}>
      <Outlet />
    </AppShell>
  );
}
