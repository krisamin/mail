import { createContext, useContext, type ReactNode } from "react";
import { translate, type DictKey } from "~/i18n";
import type { Locale } from "./locale";

// Locale context — the root loader resolves the language once (account
// preference > global setting > Accept-Language) and mounts the provider, so
// SSR and hydration always agree.

export type TFunc = (key: DictKey, params?: Record<string, string | number>) => string;

const I18nContext = createContext<{ locale: Locale; t: TFunc } | null>(null);

export const I18nProvider = ({ locale, children }: { locale: Locale; children: ReactNode }) => (
  <I18nContext.Provider value={{ locale, t: (key, params) => translate(locale, key, params) }}>
    {children}
  </I18nContext.Provider>
);

export const useI18n = () => {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be inside <I18nProvider>");
  return ctx;
};

export const useT = (): TFunc => useI18n().t;
export const useLocale = (): Locale => useI18n().locale;
