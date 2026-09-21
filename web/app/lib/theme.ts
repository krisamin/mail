/** Colour theme choice. "system" follows the OS setting. */
export type Theme = "system" | "dark" | "light";

export const THEME_LIST: Theme[] = ["system", "dark", "light"];

export const isTheme = (value: unknown): value is Theme =>
  typeof value === "string" && (THEME_LIST as string[]).includes(value);

/** Cookie that mirrors the account preference so the very first paint after a
 *  reload is already the right theme (the preference itself lives in the DB). */
export const THEME_COOKIE = "mail_theme";

/** Class applied to <html>. "system" is resolved in the browser. */
export const themeClass = (theme: Theme): string => (theme === "light" ? "light" : "dark");

/** Inline script that fixes up the class before first paint when the choice is
 *  "system" — without it a light-mode OS would flash the dark palette. */
export const THEME_BOOTSTRAP = `(()=>{try{const m=document.cookie.match(/(?:^|; )${THEME_COOKIE}=([^;]*)/);const v=m?decodeURIComponent(m[1]):"system";const dark=v==="dark"||(v!=="light"&&!window.matchMedia("(prefers-color-scheme: light)").matches);const c=document.documentElement.classList;c.toggle("dark",dark);c.toggle("light",!dark);}catch{}})()`;

/** Paint a theme choice right away, without waiting for the round-trip.
 *  The server writes the cookie and the DB; this is only what the eye sees. */
export const applyTheme = (theme: Theme) => {
  if (typeof document === "undefined") return;
  const dark =
    theme === "dark" ||
    (theme === "system" && !window.matchMedia("(prefers-color-scheme: light)").matches);
  const c = document.documentElement.classList;
  c.toggle("dark", dark);
  c.toggle("light", !dark);
};
