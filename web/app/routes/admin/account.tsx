import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/account";
import {
  ApiError,
  apiFetch,
  type AccountOverview,
  type AppPassword,
  type Domain,
} from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { requireAdmin } from "~/lib/session.server";
import { formatBytes } from "~/lib/format";
import {
  ActiveToggle,
  AddressChipList,
  AppPasswordRows,
  Badge,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  PageTitle,
  SecretReveal,
  SelectInput,
  TextInput,
} from "~/components";

// Account management — every account with its addresses and app passwords.
// Human accounts appear via JIT provisioning (first OIDC login); service
// accounts (no login, address + app password only) are created here.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);

  // Single-round-trip overview — no per-account request fan-out.
  const [overviewList, domainList] = await Promise.all([
    apiFetch<AccountOverview[]>(user.idToken, "/api/admin/account/overview").then((r) => r ?? []),
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain").then((r) => r ?? []),
  ]);
  return {
    overviewList,
    domainList: domainList.filter((d) => d.active),
  };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireAdmin(request);
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create-service": {
        await apiFetch(user.idToken, "/api/admin/account/service", {
          method: "POST",
          body: {
            email: `${String(form.get("localPart") ?? "")}@${String(form.get("domainName") ?? "")}`,
          },
        });
        return { ok: true as const };
      }
      case "create-address": {
        await apiFetch(user.idToken, `/api/admin/account/${form.get("accountId")}/address`, {
          method: "POST",
          body: {
            localPart: String(form.get("localPart") ?? ""),
            domainId: String(form.get("domainId")),
          },
        });
        return { ok: true as const };
      }
      case "delete-address": {
        await apiFetch(user.idToken, `/api/admin/address/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      case "set-quota": {
        const gb = Number(form.get("quotaGb") ?? 0);
        await apiFetch(user.idToken, `/api/admin/account/${form.get("id")}`, {
          method: "PATCH",
          body: {
            quotaSet: true,
            quotaBytes: gb > 0 ? Math.round(gb * 1024 * 1024 * 1024) : null,
          },
        });
        return { ok: true as const };
      }
      case "toggle-account": {
        await apiFetch(user.idToken, `/api/admin/account/${form.get("id")}`, {
          method: "PATCH",
          body: { active: form.get("active") === "true" },
        });
        return { ok: true as const };
      }
      case "create-pw": {
        const result = await apiFetch<{ appPassword: AppPassword; plaintext: string }>(
          user.idToken,
          `/api/admin/account/${form.get("accountId")}/app-password`,
          { method: "POST", body: { label: String(form.get("label") ?? "") } },
        );
        return { ok: true as const, plaintext: result.plaintext };
      }
      case "revoke-pw": {
        await apiFetch(user.idToken, `/api/admin/app-password/${form.get("id")}`, {
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

export default function AccountList({ loaderData, actionData }: Route.ComponentProps) {
  const { overviewList, domainList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  // Which form is in flight — label only that button, not every control.
  const pending = (intent: string, idField?: string, idValue?: string) =>
    busy &&
    nav.formData?.get("intent") === intent &&
    (idField === undefined || nav.formData?.get(idField) === String(idValue));

  return (
    <div className="flex flex-col gap-6">
      <PageTitle title={t("adminAccount.title")} description={t("adminAccount.description")} />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
        <SecretReveal title={t("adminAccount.secretIssued")} value={actionData.plaintext} />
      )}

      {/* Service account creation */}
      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create-service" />
        <div className="flex flex-1 items-center gap-1 rounded-md border border-line bg-bg-1 px-3">
          <input
            name="localPart"
            required
            placeholder="bot"
            className="flex-1 bg-transparent py-2 text-sm outline-none"
          />
          <span className="text-sm text-text-2">@</span>
          <SelectInput name="domainName" required fieldSize="sm" className="py-1">
            {domainList.map((d) => (
              <option key={d.id} value={d.name}>
                {d.name}
              </option>
            ))}
          </SelectInput>
        </div>
        <Button disabled={busy || domainList.length === 0} pending={pending("create-service")}>
          {t("adminAccount.createService")}
        </Button>
      </Form>

      <div className="flex flex-col gap-3">
        {overviewList.length === 0 ? (
          <Card>
            <EmptyText>{t("adminAccount.empty")}</EmptyText>
          </Card>
        ) : (
          overviewList.map(({ account: u, addressList, appPasswordList }) => (
            <Card key={u.id}>
              <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
                <div className="flex items-center gap-2">
                  <div>
                    <p className="text-sm font-medium">{u.email}</p>
                    {u.kind === "user" && (
                      <p className="font-mono text-[10px] text-text-2">sub: {u.subject}</p>
                    )}
                  </div>
                  {u.kind === "service" && <Badge tone="accent">{t("adminAccount.service")}</Badge>}
                </div>
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle-account" />
                  <input type="hidden" name="id" value={u.id} />
                  <input type="hidden" name="active" value={String(!u.active)} />
                  <ActiveToggle active={u.active} disabled={busy} />
                </Form>
              </div>

              {/* Storage: usage + quota */}
              <div className="flex items-center gap-2 border-b border-line px-4 py-2.5">
                <p className="text-xs text-text-2">{t("adminAccount.storage")}</p>
                <p className="text-xs">
                  {formatBytes(u.usageBytes)}
                  <span className="text-text-2">
                    {" / "}
                    {u.quotaBytes ? formatBytes(u.quotaBytes) : t("adminAccount.quotaUnlimited")}
                  </span>
                </p>
                <Form method="post" className="ml-auto flex items-center gap-1.5">
                  <input type="hidden" name="intent" value="set-quota" />
                  <input type="hidden" name="id" value={u.id} />
                  <TextInput
                    name="quotaGb"
                    fieldSize="sm"
                    className="w-20"
                    placeholder={t("adminAccount.quotaPlaceholder")}
                    defaultValue={u.quotaBytes ? String(u.quotaBytes / 1024 ** 3) : ""}
                  />
                  <Button variant="link" disabled={busy} pending={pending("set-quota", "id", u.id)}>
                    {t("common.save")}
                  </Button>
                </Form>
              </div>

              {/* Addresses: chips + inline [local]@[domain] add */}
              <div className="flex flex-col gap-2 px-4 py-3">
                <p className="text-xs text-text-2">{t("adminAccount.address")}</p>
                <AddressChipList list={addressList} busy={busy} deletable />
                <Form method="post" className="flex items-center gap-1.5">
                  <input type="hidden" name="intent" value="create-address" />
                  <input type="hidden" name="accountId" value={u.id} />
                  <TextInput
                    name="localPart"
                    required
                    placeholder={t("adminAccount.addressPlaceholder")}
                    fieldSize="sm"
                    className="w-32"
                  />
                  <span className="text-xs text-text-2">@</span>
                  <SelectInput name="domainId" required fieldSize="sm">
                    {domainList.map((d) => (
                      <option key={d.id} value={d.id}>
                        {d.name}
                      </option>
                    ))}
                  </SelectInput>
                  <Button
                    variant="link"
                    disabled={busy}
                    pending={pending("create-address", "accountId", u.id)}
                  >
                    {t("common.add")}
                  </Button>
                </Form>
              </div>

              {/* App passwords */}
              <div className="flex flex-col gap-2 border-t border-line px-4 py-3">
                <div className="flex items-center justify-between">
                  <p className="text-xs text-text-2">{t("adminAccount.appPassword")}</p>
                  <Form method="post" className="flex items-center gap-1.5">
                    <input type="hidden" name="intent" value="create-pw" />
                    <input type="hidden" name="accountId" value={u.id} />
                    <TextInput
                      name="label"
                      placeholder={t("adminAccount.labelPlaceholder")}
                      fieldSize="sm"
                      className="w-40"
                    />
                    <Button
                      variant="link"
                      disabled={busy}
                      pending={pending("create-pw", "accountId", u.id)}
                    >
                      {t("common.issue")}
                    </Button>
                  </Form>
                </div>
                <AppPasswordRows list={appPasswordList} busy={busy} />
              </div>
            </Card>
          ))
        )}
      </div>
    </div>
  );
}
