import type { Route } from "./+types/index";
import { apiFetch, type Account, type Address, type AppPassword } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import { formatBytes } from "~/lib/format";
import { Badge, ButtonLink, DataRow, Panel, PanelHeader } from "~/kit";
import { ShellContent, ShellHeader } from "~/shell/app-shell";

// Profile — who you are here, and the shortcuts into the rest of settings.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const [account, addressList, appPasswordList] = await Promise.all([
    apiFetch<Account>(user.idToken, "/api/me/account").catch(() => null),
    apiFetch<Address[]>(user.idToken, "/api/me/address").then((r) => r ?? []),
    apiFetch<AppPassword[]>(user.idToken, "/api/me/app-password").then((r) => r ?? []),
  ]);
  return {
    name: user.name,
    email: user.email,
    account,
    addressCount: addressList.length,
    activeKeyCount: appPasswordList.filter((p) => !p.revoked).length,
  };
};

export default function Profile({ loaderData }: Route.ComponentProps) {
  const { name, email, account, addressCount, activeKeyCount } = loaderData;
  const t = useT();

  return (
    <>
      <ShellHeader title={t("setting.profile")} />
      <ShellContent>
        <Panel>
          <PanelHeader title={t("setting.identity")} description={t("setting.identityHint")} />
          <div className="px-4 py-3">
            <DataRow label={t("setting.name")}>{name}</DataRow>
            <DataRow label={t("setting.email")}>
              <span className="font-mono">{email}</span>
            </DataRow>
            <DataRow label={t("setting.accountKind")}>
              {account ? (
                <Badge tone={account.kind === "service" ? "warn" : "brand"}>{account.kind}</Badge>
              ) : (
                <Badge tone="bad">{t("setting.noAccount")}</Badge>
              )}
            </DataRow>
            {account && (
              <DataRow label={t("setting.usage")}>
                {formatBytes(account.usageBytes ?? 0)}
                {account.quotaBytes
                  ? ` / ${formatBytes(account.quotaBytes)}`
                  : ` (${t("setting.unlimited")})`}
              </DataRow>
            )}
          </div>
        </Panel>

        <div className="grid gap-3 sm:grid-cols-2">
          <Panel>
            <PanelHeader
              title={t("setting.address")}
              action={
                <ButtonLink to="/setting/address" variant="subtle" size="sm">
                  {t("common.open")}
                </ButtonLink>
              }
            />
            <p className="px-4 py-3 text-2xl font-semibold text-ink tabular-nums">{addressCount}</p>
          </Panel>
          <Panel>
            <PanelHeader
              title={t("setting.appPassword")}
              action={
                <ButtonLink to="/setting/app-password" variant="subtle" size="sm">
                  {t("common.open")}
                </ButtonLink>
              }
            />
            <p className="px-4 py-3 text-2xl font-semibold text-ink tabular-nums">{activeKeyCount}</p>
          </Panel>
        </div>
      </ShellContent>
    </>
  );
}
