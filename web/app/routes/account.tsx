import { Form, Link, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import { ApiError, apiFetch, type Address, type AppPassword, type Account } from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { isAdmin, requireUser } from "~/lib/session.server";
import {
  AddressChipList,
  AppPasswordRows,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  LocaleSwitch,
  SecretReveal,
  TextInput,
} from "~/components";

// Self-service — the signed-in user's own mail account and app passwords.
// No admin group required; the Go API maps the token's sub claim to the account.
// Accounts appear via JIT provisioning on first login; address changes are admin-only.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);

  let account: Account | null = null;
  let appPasswordList: AppPassword[] = [];
  let addressList: Address[] = [];
  let noAccount = false;
  try {
    account = await apiFetch<Account>(user.idToken, "/api/me/account");
    [appPasswordList, addressList] = await Promise.all([
      apiFetch<AppPassword[]>(user.idToken, "/api/me/app-password").then((r) => r ?? []),
      apiFetch<Address[]>(user.idToken, "/api/me/address").then((r) => r ?? []),
    ]);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) {
      noAccount = true; // not provisioned yet (shouldn't happen after a normal login)
    } else {
      throw e;
    }
  }
  return {
    name: user.name,
    email: user.email,
    admin: isAdmin(user),
    account,
    appPasswordList,
    addressList,
    noAccount,
  };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-pw": {
        const result = await apiFetch<{ appPassword: AppPassword; plaintext: string }>(
          user.idToken,
          "/api/me/app-password",
          { method: "POST", body: { label: String(form.get("label") ?? "") } },
        );
        return { ok: true as const, plaintext: result.plaintext };
      }
      case "revoke-pw": {
        await apiFetch(user.idToken, `/api/me/app-password/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      default:
        return {
          ok: false as const,
          error: translate(await getLocale(request), "common.unknownIntent"),
        };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

export default function Account({ loaderData, actionData }: Route.ComponentProps) {
  const { name, email, admin, appPasswordList, addressList, noAccount } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  const active = appPasswordList.filter((p) => !p.revoked);
  const revoked = appPasswordList.filter((p) => p.revoked);

  return (
    <div className="min-h-dvh">
      <header className="border-b border-line bg-bg-1">
        <div className="mx-auto flex max-w-3xl items-center justify-between px-4 py-3">
          <Link to="/" className="text-sm font-bold tracking-tight">
            mail <span className="text-accent">account</span>
          </Link>
          <div className="flex items-center gap-3">
            {admin && (
              <Link to="/admin" className="text-xs text-text-2 hover:text-text-1">
                {t("nav.adminConsole")}
              </Link>
            )}
            <span className="text-xs text-text-2">{name}</span>
            <LocaleSwitch />
            <Link to="/logout" className="text-xs text-text-2 hover:text-text-1">
              {t("common.logout")}
            </Link>
          </div>
        </div>
      </header>

      <main className="mx-auto flex max-w-3xl flex-col gap-6 px-4 py-6">
        {noAccount ? (
          <Card className="p-6 text-center">
            <p className="text-sm text-text-1">{t("account.noAccount", { email })}</p>
            <p className="mt-1 text-xs text-text-2">{t("account.noAccountHint")}</p>
          </Card>
        ) : (
          <>
            <Card className="p-4">
              <h1 className="text-lg font-bold">{t("account.title")}</h1>
              <p className="mt-1 font-mono text-sm text-text-1">{email}</p>
              {addressList.length > 0 && (
                <div className="mt-2">
                  <p className="text-xs text-text-2">{t("account.addressIntro")}</p>
                  <div className="mt-1">
                    <AddressChipList list={addressList} />
                  </div>
                  <p className="mt-1.5 text-xs text-text-2">{t("account.addressAdminHint")}</p>
                </div>
              )}
              <p className="mt-2 text-xs text-text-2">{t("account.appPasswordHint")}</p>
            </Card>

            <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

            {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
              <SecretReveal title={t("account.secretIssued")} value={actionData.plaintext} />
            )}

            <section className="flex flex-col gap-3">
              <h2 className="text-sm font-medium text-text-1">{t("account.appPassword")}</h2>
              <Form method="post" className="flex gap-2">
                <input type="hidden" name="intent" value="create-pw" />
                <TextInput
                  name="label"
                  required
                  placeholder={t("account.labelPlaceholder")}
                  className="flex-1"
                />
                <Button disabled={busy}>{t("common.issue")}</Button>
              </Form>

              {active.length === 0 ? (
                <Card>
                  <EmptyText>{t("account.noActivePassword")}</EmptyText>
                </Card>
              ) : (
                <Card className="px-4">
                  <AppPasswordRows list={active} busy={busy} />
                </Card>
              )}

              {revoked.length > 0 && (
                <details className="text-xs text-text-2">
                  <summary className="cursor-pointer">
                    {t("account.revokedCount", { count: revoked.length })}
                  </summary>
                  <ul className="mt-2 flex flex-col gap-1">
                    {revoked.map((p) => (
                      <li key={p.id} className="rounded border border-line bg-bg-1 px-3 py-2">
                        {p.label || t("mail.noLabel")} —{" "}
                        {t("mail.issuedAt", { date: p.createdAt.slice(0, 10) })}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </section>
          </>
        )}
      </main>
    </div>
  );
}
