import { useFetcher } from "react-router";
import type { Route } from "./+types/appearance";
import { useT } from "~/lib/i18n";
import { readPreference } from "~/lib/preference.server";
import { requireUser } from "~/lib/session.server";
import { LOCALE_LABEL_MAP, LOCALE_LIST } from "~/lib/locale";
import { applyTheme, THEME_LIST, type Theme } from "~/lib/theme";
import { DarkIcon, LightIcon, Panel, PanelHeader, SystemThemeIcon } from "~/kit";
import { ShellContent, ShellHeader } from "~/shell/app-shell";

// Appearance — theme and display language, per account (they follow you to
// any browser because the preference lives in the DB).

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  return { preference: await readPreference(user) };
};

const themeIconMap = {
  system: SystemThemeIcon,
  dark: DarkIcon,
  light: LightIcon,
} as const;

export default function Appearance({ loaderData }: Route.ComponentProps) {
  const { preference } = loaderData;
  const t = useT();
  const fetcher = useFetcher();

  // optimistic: the rail and this page both post to /preference
  const pendingTheme = fetcher.formData?.get("theme");
  const theme = (pendingTheme ? String(pendingTheme) : preference.theme) as Theme;
  const pendingLocale = fetcher.formData?.get("locale");
  const locale = pendingLocale ? String(pendingLocale) : preference.locale;

  const save = (next: { theme?: string; locale?: string }) => {
    // paint first, persist second — the choice should land under the cursor,
    // not one network round-trip later
    if (next.theme) applyTheme(next.theme as Theme);
    fetcher.submit({ ...next }, { method: "post", action: "/preference" });
  };

  return (
    <>
      <ShellHeader title={t("setting.appearance")} />
      <ShellContent>
        <Panel>
          <PanelHeader title={t("setting.theme")} description={t("setting.themeHint")} />
          <div className="flex flex-wrap gap-2 px-4 py-4">
            {THEME_LIST.map((value) => {
              const Icon = themeIconMap[value];
              const active = theme === value;
              return (
                <button
                  key={value}
                  type="button"
                  onClick={() => save({ theme: value })}
                  aria-pressed={active}
                  className={`flex w-28 flex-col items-center gap-2 rounded-lg border px-3 py-4 text-xs transition-colors duration-100 ${
                    active
                      ? "border-brand bg-brand-soft text-brand"
                      : "border-line text-ink-2 hover:border-line-strong hover:text-ink"
                  }`}
                >
                  <Icon className="size-5" />
                  {t(`theme.${value}`)}
                </button>
              );
            })}
          </div>
        </Panel>

        <Panel>
          <PanelHeader title={t("setting.language")} description={t("setting.languageHint")} />
          <div className="flex flex-col px-1 py-1">
            {(["auto", ...LOCALE_LIST] as const).map((value) => {
              const active = locale === value;
              return (
                <button
                  key={value}
                  type="button"
                  onClick={() => save({ locale: value })}
                  className={`flex items-center justify-between rounded-md px-3 py-2.5 text-sm transition-colors duration-100 ${
                    active ? "bg-raised text-ink" : "text-ink-2 hover:bg-raised/60"
                  }`}
                >
                  {value === "auto" ? t("setting.languageAuto") : LOCALE_LABEL_MAP[value]}
                  {active && <span className="text-xs text-brand">{t("common.selected")}</span>}
                </button>
              );
            })}
          </div>
        </Panel>
      </ShellContent>
    </>
  );
}
