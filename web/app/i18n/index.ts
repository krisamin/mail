import type { Locale } from "~/lib/locale";
import { en } from "./en";
import { ja } from "./ja";
import { ko } from "./ko";

export type DictKey = keyof typeof ko;

// The whole dictionary set is tiny (~8 KB), so all three ship statically —
// SSR needs every locale on the server anyway, and it keeps hydration simple.
const DICT_MAP: Record<Locale, Record<DictKey, string>> = { ko, en, ja };

/** Key → translated string. Missing keys fall back to ko, then the key itself. */
export const translate = (
  locale: Locale,
  key: DictKey,
  params?: Record<string, string | number>,
): string => {
  const raw = DICT_MAP[locale][key] ?? ko[key] ?? key;
  if (!params) return raw;
  return raw.replace(/\{\{(\w+)\}\}/g, (_, k) => String(params[k] ?? `{{${k}}}`));
};
