import { Form, useFetcher, useNavigation } from "react-router";
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
import { ShellContent, ShellHeader } from "~/shell/app-shell";
import { getLocale } from "~/lib/locale.server";
import { requireAdmin } from "~/lib/session.server";
import { formatBytes } from "~/lib/format";
import {
  ActiveToggle,
  AddressChipList,
  AppPasswordRow,
  Badge,
  Button,
  Panel,
  EmptyText,
  ErrorBanner,
  SecretReveal,
  SelectInput,
  Switch,
  TextInput,
} from "~/kit";
import type { Account } from "~/lib/api.server";

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
      case "set-permission": {
        const limitRaw = String(form.get("dailySendLimit") ?? "").trim();
        const limit = limitRaw === "" ? null : Number(limitRaw);
        if (limit !== null && (!Number.isFinite(limit) || limit < 0)) {
          return { ok: false as const, error: translate(await getLocale(request), "common.invalidValue") };
        }
        await apiFetch(user.idToken, `/api/admin/account/${form.get("id")}/permission`, {
          method: "PUT",
          body: {
            canSend: form.get("canSend") === "on",
            canSendExternal: form.get("canSendExternal") === "on",
            canReceiveExternal: form.get("canReceiveExternal") === "on",
            dailySendLimit: limit,
          },
        });
        return { ok: true as const };
      }
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


/** Permission switches for one account — each flip saves on its own. */
const PermissionRow = ({ account }: { account: Account }) => {
  const t = useT();
  const fetcher = useFetcher();
  const busy = fetcher.state !== "idle";

  // optimistic: show what is in flight, not the stale loader value
  const pending = fetcher.formData;
  const read = (field: string, fallback: boolean) =>
    pending ? pending.get(field) === "on" : fallback;
  const canSend = read("canSend", account.canSend);
  const canSendExternal = read("canSendExternal", account.canSendExternal);
  const canReceiveExternal = read("canReceiveExternal", account.canReceiveExternal);

  const save = (patch: Partial<Record<string, string>>) => {
    fetcher.submit(
      {
        intent: "set-permission",
        id: account.id,
        canSend: canSend ? "on" : "",
        canSendExternal: canSendExternal ? "on" : "",
        canReceiveExternal: canReceiveExternal ? "on" : "",
        dailySendLimit: account.dailySendLimit ? String(account.dailySendLimit) : "",
        ...patch,
      },
      { method: "post" },
    );
  };

  return (
    <div className="border-b border-line px-4 py-2.5">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-ink-3">{t("permission.title")}</p>
        <p className="text-xs text-ink-faint">
          {t("permission.sentToday", { count: account.sentToday })}
          {account.dailySendLimit ? ` / ${account.dailySendLimit}` : ""}
        </p>
      </div>
      <div className="mt-1 grid gap-x-6 sm:grid-cols-2">
        <Switch
          checked={canSend}
          disabled={busy}
          label={t("permission.canSend")}
          hint={t("permission.canSendHint")}
          onChange={(next) => save({ canSend: next ? "on" : "" })}
        />
        <Switch
          checked={canSendExternal}
          disabled={busy || !canSend}
          label={t("permission.canSendExternal")}
          hint={t("permission.canSendExternalHint")}
          onChange={(next) => save({ canSendExternal: next ? "on" : "" })}
        />
        <Switch
          checked={canReceiveExternal}
          disabled={busy}
          label={t("permission.canReceiveExternal")}
          hint={t("permission.canReceiveExternalHint")}
          onChange={(next) => save({ canReceiveExternal: next ? "on" : "" })}
        />
        <fetcher.Form
          method="post"
          className="flex items-center gap-2 py-1.5"
          onSubmit={(e) => {
            // keep the switches as they are on screen while saving the limit
            const form = e.currentTarget;
            form.canSend.value = canSend ? "on" : "";
            form.canSendExternal.value = canSendExternal ? "on" : "";
            form.canReceiveExternal.value = canReceiveExternal ? "on" : "";
          }}
        >
          <input type="hidden" name="intent" value="set-permission" />
          <input type="hidden" name="id" value={account.id} />
          <input type="hidden" name="canSend" />
          <input type="hidden" name="canSendExternal" />
          <input type="hidden" name="canReceiveExternal" />
          <span className="text-sm text-ink">{t("permission.dailyLimit")}</span>
          <TextInput
            name="dailySendLimit"
            fieldSize="sm"
            className="w-24"
            placeholder={t("permission.dailyLimitPlaceholder")}
            defaultValue={account.dailySendLimit ? String(account.dailySendLimit) : ""}
          />
          <Button variant="subtle" size="sm" pending={busy}>
            {t("common.save")}
          </Button>
        </fetcher.Form>
      </div>
    </div>
  );
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
    <>
      <ShellHeader title={t("adminAccount.title")} />
      <ShellContent>

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {actionData?.ok && "plaintext" in actionData && actionData.plaintext && (
        <SecretReveal title={t("adminAccount.secretIssued")} value={actionData.plaintext} />
      )}

      {/* Service account creation */}
      <Form method="post" className="flex gap-2">
        <input type="hidden" name="intent" value="create-service" />
        <div className="flex flex-1 items-center gap-1 rounded-md border border-line bg-surface px-3">
          <input
            name="localPart"
            required
            placeholder="bot"
            className="flex-1 bg-transparent py-2 text-sm outline-none"
          />
          <span className="text-sm text-ink-3">@</span>
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
          <Panel>
            <EmptyText>{t("adminAccount.empty")}</EmptyText>
          </Panel>
        ) : (
          overviewList.map(({ account: u, addressList, appPasswordList }) => (
            <Panel key={u.id}>
              <div className="flex items-center justify-between border-b border-line px-4 py-2.5">
                <div className="flex items-center gap-2">
                  <div>
                    <p className="text-sm font-medium">{u.email}</p>
                    {u.kind === "user" && (
                      <p className="font-mono text-[10px] text-ink-3">sub: {u.subject}</p>
                    )}
                  </div>
                  {u.kind === "service" && <Badge tone="brand">{t("adminAccount.service")}</Badge>}
                </div>
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle-account" />
                  <input type="hidden" name="id" value={u.id} />
                  <input type="hidden" name="active" value={String(!u.active)} />
                  <ActiveToggle active={u.active} disabled={busy} />
                </Form>
              </div>

              <PermissionRow account={u} />

              {/* Storage: usage + quota */}
              <div className="flex items-center gap-2 border-b border-line px-4 py-2.5">
                <p className="text-xs text-ink-3">{t("adminAccount.storage")}</p>
                <p className="text-xs">
                  {formatBytes(u.usageBytes)}
                  <span className="text-ink-3">
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
                  <Button variant="ghost" size="sm" disabled={busy} pending={pending("set-quota", "id", u.id)}>
                    {t("common.save")}
                  </Button>
                </Form>
              </div>

              {/* Addresses: chips + inline [local]@[domain] add */}
              <div className="flex flex-col gap-2 px-4 py-3">
                <p className="text-xs text-ink-3">{t("adminAccount.address")}</p>
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
                  <span className="text-xs text-ink-3">@</span>
                  <SelectInput name="domainId" required fieldSize="sm">
                    {domainList.map((d) => (
                      <option key={d.id} value={d.id}>
                        {d.name}
                      </option>
                    ))}
                  </SelectInput>
                  <Button
                    variant="ghost" size="sm"
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
                  <p className="text-xs text-ink-3">{t("adminAccount.appPassword")}</p>
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
                      variant="ghost" size="sm"
                      disabled={busy}
                      pending={pending("create-pw", "accountId", u.id)}
                    >
                      {t("common.issue")}
                    </Button>
                  </Form>
                </div>
                <AppPasswordRow list={appPasswordList} busy={busy} />
              </div>
            </Panel>
          ))
        )}
      </div>
      </ShellContent>
    </>
  );
}
