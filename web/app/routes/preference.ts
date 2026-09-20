import { data, redirect } from "react-router";
import type { Route } from "./+types/preference";
import { apiFetch } from "~/lib/api.server";
import { isLocaleSetting } from "~/lib/locale.server";
import { themeCookieHeader } from "~/lib/preference.server";
import { requireUser } from "~/lib/session.server";
import { isTheme } from "~/lib/theme";

// Resource route for UI preference writes — the rail's quick theme switch and
// the appearance settings form both post here, so the persistence rule (DB is
// the truth, cookie mirrors it for first paint) lives in exactly one place.

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const theme = String(form.get("theme") ?? "");
  const locale = String(form.get("locale") ?? "");

  const current = await apiFetch<{ theme: string; locale: string }>(
    user.idToken,
    "/api/me/preference",
  );
  const next = {
    theme: isTheme(theme) ? theme : current.theme,
    locale: isLocaleSetting(locale) ? locale : current.locale,
  };
  await apiFetch(user.idToken, "/api/me/preference", { method: "PUT", body: next });

  const headers = { "Set-Cookie": themeCookieHeader(isTheme(next.theme) ? next.theme : "system") };
  const back = String(form.get("returnTo") ?? "");
  if (back.startsWith("/")) throw redirect(back, { headers });
  return data({ ok: true }, { headers });
};

// Nothing renders here — a direct visit goes to the settings page.
export const loader = () => redirect("/setting/appearance");
