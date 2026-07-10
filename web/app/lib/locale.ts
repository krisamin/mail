/** Display language — supported locale codes. */
export type Locale = "ko" | "en" | "ja";

export const LOCALE_LIST: Locale[] = ["ko", "en", "ja"];

export const LOCALE_LABEL_MAP: Record<Locale, string> = {
  ko: "한국어",
  en: "English",
  ja: "日本語",
};

export const isLocale = (value: unknown): value is Locale =>
  typeof value === "string" && (LOCALE_LIST as string[]).includes(value);
