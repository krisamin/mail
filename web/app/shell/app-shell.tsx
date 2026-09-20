import { NavLink, useFetcher } from "react-router";
import type { ReactNode } from "react";
import {
  AdminIcon,
  Avatar,
  DarkIcon,
  LightIcon,
  MailIcon,
  Menu,
  MenuDivider,
  MenuItem,
  SettingIcon,
  SignOutIcon,
  SystemThemeIcon,
} from "~/kit";
import { useT } from "~/lib/i18n";
import type { Theme } from "~/lib/theme";
import { BrandMark } from "./brand";

// The one shell every signed-in screen lives in.
//
// Before this, mail/account/admin each rendered their own header (three
// different brand words, three different ways back) — the app felt like three
// sites. Now there is a single rail: area switch on the left, everything else
// inside the area, account menu at the bottom.

export type ShellUser = {
  name: string;
  email: string;
  admin: boolean;
  theme: Theme;
};

const railItemClass = (isActive: boolean): string =>
  `flex size-10 items-center justify-center rounded-lg transition-colors duration-100 ${
    isActive ? "bg-brand-soft text-brand" : "text-ink-3 hover:bg-raised hover:text-ink"
  }`;

const RailLink = ({ to, label, children }: { to: string; label: string; children: ReactNode }) => (
  <NavLink to={to} title={label} aria-label={label} className={({ isActive }) => railItemClass(isActive)}>
    {children}
  </NavLink>
);

/** Rail + optional secondary column + content. */
export const AppShell = ({
  user,
  sidebar,
  children,
}: {
  user: ShellUser;
  /** Secondary column (folders, setting sections, admin sections). */
  sidebar?: ReactNode;
  children: ReactNode;
}) => {
  const t = useT();
  const fetcher = useFetcher();

  const setTheme = (theme: Theme) =>
    fetcher.submit({ theme }, { method: "post", action: "/preference" });

  return (
    <div className="flex h-dvh w-full overflow-hidden bg-canvas">
      {/* rail — becomes a bottom bar on narrow screens */}
      <div className="fixed inset-x-0 bottom-0 z-30 flex h-14 shrink-0 items-center justify-around border-t border-line bg-surface px-2 md:static md:h-auto md:w-14 md:flex-col md:justify-start md:gap-1 md:border-t-0 md:border-r md:px-0 md:py-3">
        <div className="hidden md:mb-2 md:block">
          <BrandMark />
        </div>
        <RailLink to="/mail" label={t("nav.mail")}>
          <MailIcon className="size-5" />
        </RailLink>
        <RailLink to="/setting" label={t("nav.setting")}>
          <SettingIcon className="size-5" />
        </RailLink>
        {user.admin && (
          <RailLink to="/admin" label={t("nav.admin")}>
            <AdminIcon className="size-5" />
          </RailLink>
        )}
        <div className="md:mt-auto">
          <Menu
            align="start"
            trigger={({ toggle }) => (
              <button
                type="button"
                onClick={toggle}
                title={user.email}
                aria-label={t("nav.account")}
                className="flex size-10 items-center justify-center rounded-lg hover:bg-raised"
              >
                <Avatar label={user.name || user.email} seed={user.email} size={26} />
              </button>
            )}
          >
            {(close) => (
              <>
                <div className="px-3 py-2">
                  <p className="truncate text-sm font-medium text-ink">{user.name}</p>
                  <p className="truncate text-xs text-ink-3">{user.email}</p>
                </div>
                <MenuDivider />
                <MenuItem
                  onClick={() => {
                    setTheme(user.theme === "dark" ? "light" : user.theme === "light" ? "system" : "dark");
                    close();
                  }}
                >
                  {user.theme === "dark" ? (
                    <DarkIcon className="size-4" />
                  ) : user.theme === "light" ? (
                    <LightIcon className="size-4" />
                  ) : (
                    <SystemThemeIcon className="size-4" />
                  )}
                  {t(`theme.${user.theme}`)}
                </MenuItem>
                <MenuDivider />
                <MenuItem as="div">
                  <a href="/logout" className="flex flex-1 items-center gap-2.5">
                    <SignOutIcon className="size-4" />
                    {t("common.signOut")}
                  </a>
                </MenuItem>
              </>
            )}
          </Menu>
        </div>
      </div>

      {sidebar && (
        <div className="hidden w-56 shrink-0 flex-col overflow-y-auto border-r border-line bg-surface scroll-thin md:flex">
          {sidebar}
        </div>
      )}

      <main className="flex min-w-0 flex-1 flex-col overflow-hidden pb-14 md:pb-0">{children}</main>
    </div>
  );
};

/** Scrollable content area with the standard page padding. */
export const ShellContent = ({ children }: { children: ReactNode }) => (
  <div className="flex-1 overflow-y-auto scroll-thin">
    <div className="mx-auto flex max-w-4xl flex-col gap-5 px-5 py-6">{children}</div>
  </div>
);

/** Sticky area header — title row above the scrollable content. */
export const ShellHeader = ({ title, action }: { title: ReactNode; action?: ReactNode }) => (
  <header className="flex h-13 shrink-0 items-center justify-between gap-3 border-b border-line px-5 py-3">
    <h1 className="truncate text-sm font-semibold text-ink">{title}</h1>
    {action}
  </header>
);
