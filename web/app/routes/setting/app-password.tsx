import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/app-password";
import { ApiError, apiFetch, type AppPassword } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import {
  Banner,
  Button,
  EmptyState,
  ErrorBanner,
  KeyIcon,
  Panel,
  SecretReveal,
  TextInput,
  TimeText,
} from "~/kit";
import { ShellContent, ShellHeader } from "~/shell/app-shell";

// App passwords — what mail apps authenticate with (IMAP/SMTP), since mail
// clients cannot do OAuth against a private IdP.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const list = await apiFetch<AppPassword[]>(user.idToken, "/api/me/app-password").then((r) => r ?? []);
  return { list };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "");
  try {
    if (intent === "create") {
      const result = await apiFetch<{ appPassword: AppPassword; plaintext: string }>(
        user.idToken,
        "/api/me/app-password",
        { method: "POST", body: { label: String(form.get("label") ?? "") } },
      );
      return { ok: true as const, plaintext: result.plaintext };
    }
    if (intent === "revoke") {
      await apiFetch(user.idToken, `/api/me/app-password/${form.get("id")}`, { method: "DELETE" });
      return { ok: true as const };
    }
    return { ok: false as const, error: "unknown intent" };
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function AppPasswordPage({ loaderData, actionData }: Route.ComponentProps) {
  const { list } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  const activeList = list.filter((p) => !p.revoked);
  const revokedList = list.filter((p) => p.revoked);

  return (
    <>
      <ShellHeader title={t("setting.appPassword")} />
      <ShellContent>
        <Banner tone="info">{t("setting.appPasswordHint")}</Banner>
        <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />
        {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
          <SecretReveal title={t("setting.secretIssued")} value={actionData.plaintext} />
        )}

        <Form method="post" className="flex gap-2">
          <input type="hidden" name="intent" value="create" />
          <TextInput name="label" required placeholder={t("setting.labelPlaceholder")} className="flex-1" />
          <Button pending={busy}>{t("setting.issue")}</Button>
        </Form>

        <Panel>
          {activeList.length === 0 ? (
            <EmptyState
              icon={<KeyIcon className="size-7" strokeWidth={1.5} />}
              title={t("setting.noAppPassword")}
            />
          ) : (
            <ul className="divide-y divide-line">
              {activeList.map((item) => (
                <li key={item.id} className="flex items-center gap-3 px-4 py-3">
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm text-ink">{item.label || t("setting.noLabel")}</p>
                    <p className="text-xs text-ink-3">
                      {t("setting.issuedAt")} <TimeText value={item.createdAt} mode="full" />
                      {item.lastUsed && (
                        <>
                          {" · "}
                          {t("setting.lastUsed")} <TimeText value={item.lastUsed} />
                        </>
                      )}
                    </p>
                  </div>
                  <Form method="post">
                    <input type="hidden" name="intent" value="revoke" />
                    <input type="hidden" name="id" value={item.id} />
                    <Button variant="danger" size="sm" pending={busy} confirmMessage={t("setting.confirmRevoke")}>
                      {t("setting.revoke")}
                    </Button>
                  </Form>
                </li>
              ))}
            </ul>
          )}
        </Panel>

        {revokedList.length > 0 && (
          <details className="text-xs text-ink-3">
            <summary className="cursor-pointer">
              {t("setting.revokedCount", { count: revokedList.length })}
            </summary>
            <ul className="mt-2 flex flex-col gap-1">
              {revokedList.map((item) => (
                <li key={item.id} className="rounded-md border border-line bg-surface px-3 py-2">
                  {item.label || t("setting.noLabel")}
                </li>
              ))}
            </ul>
          </details>
        )}
      </ShellContent>
    </>
  );
}
