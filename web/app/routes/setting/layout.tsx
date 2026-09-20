import { Outlet } from "react-router";
import type { Route } from "./+types/layout";
import { useT } from "~/lib/i18n";
import { readPreference, themeFromCookie } from "~/lib/preference.server";
import { isAdmin, requireUser } from "~/lib/session.server";
import { AppShell } from "~/shell/app-shell";
import { SideNav, SideNavItem } from "~/shell/side-nav";
import { AddressIcon, AppearanceIcon, FilterIcon, KeyIcon, UserIcon } from "~/kit";

// Settings area — everything that used to be scattered between /account, the
// bottom of the mail sidebar and "nowhere" (the display language had no UI at
// all) now lives under one column.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const preference = await readPreference(user);
  return {
    user: {
      name: user.name,
      email: user.email,
      admin: isAdmin(user),
      theme: preference.theme === "system" ? themeFromCookie(request) : preference.theme,
    },
  };
};

export default function SettingLayout({ loaderData }: Route.ComponentProps) {
  const t = useT();
  const sidebar = (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-4 pt-4 pb-1">
        <p className="text-sm font-semibold text-ink">{t("setting.title")}</p>
      </div>
      <SideNav>
        <SideNavItem to="/setting" end icon={<UserIcon className="size-4" />} label={t("setting.profile")} />
        <SideNavItem to="/setting/address" icon={<AddressIcon className="size-4" />} label={t("setting.address")} />
        <SideNavItem
          to="/setting/app-password"
          icon={<KeyIcon className="size-4" />}
          label={t("setting.appPassword")}
        />
        <SideNavItem to="/setting/filter" icon={<FilterIcon className="size-4" />} label={t("setting.filter")} />
        <SideNavItem
          to="/setting/appearance"
          icon={<AppearanceIcon className="size-4" />}
          label={t("setting.appearance")}
        />
      </SideNav>
    </div>
  );
  return (
    <AppShell user={loaderData.user} sidebar={sidebar}>
      <Outlet />
    </AppShell>
  );
}
