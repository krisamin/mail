import { createCookie } from "react-router";
import { isLocale, type Locale } from "./locale";

// Locale cookie — SSR needs to know the language before render, so the
// preference lives in a cookie (not localStorage). No cookie → detect
// from Accept-Language, falling back to English.
export const localeCookie = createCookie("mail_locale", {
  path: "/",
  sameSite: "lax",
  maxAge: 60 * 60 * 24 * 365, // 1y
});

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
  const saved = await localeCookie.parse(request.headers.get("Cookie"));
  if (isLocale(saved)) return saved;
  return detectLocale(request);
};
