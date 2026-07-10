import { isLocale, type Locale } from "./locale";

// Display language is a GLOBAL admin-managed setting stored in the DB
// (GET /api/setting/locale — public read, admin write). "auto" means
// per-visitor Accept-Language detection; a fixed locale applies to everyone.
// Cached in-process briefly so every SSR request doesn't hit the API.

const API_BASE = process.env.MAIL_API_URL ?? "http://localhost:8080";

export type LocaleSetting = Locale | "auto";

export const isLocaleSetting = (value: unknown): value is LocaleSetting =>
  value === "auto" || isLocale(value);

let cache: { value: LocaleSetting; at: number } | null = null;
const CACHE_TTL_MS = 30_000;

export const getLocaleSetting = async (): Promise<LocaleSetting> => {
  if (cache && Date.now() - cache.at < CACHE_TTL_MS) return cache.value;
  try {
    const res = await fetch(`${API_BASE}/api/setting/locale`);
    const body = (await res.json()) as { locale?: string };
    const value: LocaleSetting = isLocaleSetting(body.locale) ? body.locale : "auto";
    cache = { value, at: Date.now() };
    return value;
  } catch {
    // API unreachable — keep whatever we knew, else fall back to detection.
    return cache?.value ?? "auto";
  }
};

/** Refresh the cache immediately after an admin changes the setting. */
export const primeLocaleSetting = (value: LocaleSetting) => {
  cache = { value, at: Date.now() };
};

const detectLocale = (request: Request): Locale => {
  const header = request.headers.get("Accept-Language") ?? "";
  for (const part of header.split(",")) {
    const lang = part.split(";")[0]?.trim().toLowerCase() ?? "";
    if (lang.startsWith("ko")) return "ko";
    if (lang.startsWith("ja")) return "ja";
    if (lang.startsWith("en")) return "en";
  }
  return "en";
};

export const getLocale = async (request: Request): Promise<Locale> => {
  const setting = await getLocaleSetting();
  if (setting !== "auto") return setting;
  return detectLocale(request);
};
