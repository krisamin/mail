import { isLocaleSetting, type LocaleSetting } from "./locale.server";
import { isTheme, THEME_COOKIE, type Theme } from "./theme";
import { apiFetch } from "./api.server";
import type { SessionUser } from "./session.server";

// Per-account UI preference (theme + display language). The DB is the source
// of truth so the choice follows the person to any browser; a cookie mirror
// keeps the first paint correct before the loader data arrives.

export type Preference = { theme: Theme; locale: LocaleSetting };

export const DEFAULT_PREFERENCE: Preference = { theme: "system", locale: "auto" };

export const readPreference = async (user: SessionUser | null): Promise<Preference> => {
  if (!user) return DEFAULT_PREFERENCE;
  try {
    const body = await apiFetch<{ theme: string; locale: string }>(user.idToken, "/api/me/preference");
    return {
      theme: isTheme(body.theme) ? body.theme : "system",
      locale: isLocaleSetting(body.locale) ? body.locale : "auto",
    };
  } catch {
    // A signed-in user whose account row is missing (or an API hiccup) still
    // gets a usable UI — preference is decoration, not authorisation.
    return DEFAULT_PREFERENCE;
  }
};

/** Theme cookie — read on the server for SSR, written when the user changes it. */
export const themeFromCookie = (request: Request): Theme => {
  const raw = request.headers.get("Cookie") ?? "";
  const hit = raw.match(new RegExp(`(?:^|; )${THEME_COOKIE}=([^;]*)`));
  const value = hit?.[1] ? decodeURIComponent(hit[1]) : "";
  return isTheme(value) ? value : "system";
};

export const themeCookieHeader = (theme: Theme): string =>
  `${THEME_COOKIE}=${encodeURIComponent(theme)}; Path=/; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax`;
