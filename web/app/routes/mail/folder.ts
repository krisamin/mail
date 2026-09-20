import { data } from "react-router";
import type { Route } from "./+types/folder";
import { ApiError, apiFetch } from "~/lib/api.server";
import { requireUser } from "~/lib/session.server";

// Mailbox management as a resource route: the folder form in the sidebar is
// rendered by a layout, and a layout owns no URL of its own to post to.

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "create");
  const name = String(form.get("name") ?? "").trim();
  if (!name) return data({ ok: false as const, error: "name required" }, { status: 400 });

  try {
    if (intent === "rename") {
      const next = String(form.get("newName") ?? "").trim();
      await apiFetch(user.idToken, `/api/me/mailbox/${encodeURIComponent(name)}`, {
        method: "PATCH",
        body: { name: next },
      });
      return { ok: true as const };
    }
    if (intent === "delete") {
      await apiFetch(user.idToken, `/api/me/mailbox/${encodeURIComponent(name)}`, {
        method: "DELETE",
      });
      return { ok: true as const };
    }
    await apiFetch(user.idToken, "/api/me/mailbox", { method: "POST", body: { name } });
    return { ok: true as const };
  } catch (e) {
    if (e instanceof ApiError) return data({ ok: false as const, error: e.message }, { status: 400 });
    throw e;
  }
};
