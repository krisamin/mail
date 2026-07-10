import { redirect } from "react-router";
import type { Route } from "./+types/locale";
import { isLocale } from "~/lib/locale";
import { localeCookie } from "~/lib/locale.server";

// POST /locale — persist the language choice and bounce back.
export const action = async ({ request }: Route.ActionArgs) => {
  const form = await request.formData();
  const locale = String(form.get("locale") ?? "");
  const returnTo = String(form.get("returnTo") ?? "/");
  if (!isLocale(locale)) return redirect(returnTo);
  return redirect(returnTo, {
    headers: { "Set-Cookie": await localeCookie.serialize(locale) },
  });
};
