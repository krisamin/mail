import { Form, useFetcher, useNavigation } from "react-router";
import type { Route } from "./+types/group";
import { ApiError, apiFetch, type Account, type Grant, type Group } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireAdmin } from "~/lib/session.server";
import { ShellContent, ShellHeader } from "~/shell/app-shell";
import {
  Badge,
  Button,
  EmptyText,
  ErrorBanner,
  IconButton,
  Panel,
  SelectInput,
  TextInput,
} from "~/kit";
import { ChevronDownIcon, ChevronUpIcon, TrashIcon } from "~/kit/icon";

// Permission groups.
//
// The default group is the floor everybody stands on. Groups above it stack,
// and for each rule the highest group with an opinion wins — a group that
// says nothing ("inherit") passes the question down. That is the same shape
// people already know from Discord roles, so the screen is a list ordered
// top to bottom with arrows to move a group up or down.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);
  const [groupList, accountList] = await Promise.all([
    apiFetch<Group[]>(user.idToken, "/api/admin/group").then((r) => r ?? []),
    apiFetch<Account[]>(user.idToken, "/api/admin/account").then((r) => r ?? []),
  ]);
  return { groupList, accountList };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireAdmin(request);
  const form = await request.formData();
  const intent = String(form.get("intent") ?? "");
  const id = String(form.get("id") ?? "");

  try {
    switch (intent) {
      case "create":
        await apiFetch(user.idToken, "/api/admin/group", {
          method: "POST",
          body: { name: String(form.get("name") ?? "") },
        });
        return { ok: true as const };
      case "save": {
        const limitRaw = String(form.get("dailySendLimit") ?? "").trim();
        await apiFetch(user.idToken, `/api/admin/group/${id}`, {
          method: "PATCH",
          body: {
            name: String(form.get("name") ?? ""),
            canSend: String(form.get("canSend") ?? "inherit"),
            canSendExternal: String(form.get("canSendExternal") ?? "inherit"),
            canReceiveExternal: String(form.get("canReceiveExternal") ?? "inherit"),
            dailySendLimit: limitRaw === "" ? null : Number(limitRaw),
          },
        });
        return { ok: true as const };
      }
      case "move":
        await apiFetch(user.idToken, `/api/admin/group/${id}/move`, {
          method: "POST",
          body: { up: form.get("up") === "true" },
        });
        return { ok: true as const };
      case "delete":
        await apiFetch(user.idToken, `/api/admin/group/${id}`, { method: "DELETE" });
        return { ok: true as const };
      case "member":
        await apiFetch(user.idToken, `/api/admin/group/${id}/member`, {
          method: "PUT",
          body: { accountIdList: form.getAll("accountId").map(String) },
        });
        return { ok: true as const };
      default:
        return { ok: false as const, error: "unknown intent" };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

/** allow / deny / inherit, as a three-way select. */
const GrantSelect = ({
  name,
  value,
  label,
  isDefault,
}: {
  name: string;
  value: Grant;
  label: string;
  isDefault: boolean;
}) => {
  const t = useT();
  return (
    <label className="flex items-center justify-between gap-2 py-1">
      <span className="text-sm text-ink-2">{label}</span>
      <SelectInput name={name} defaultValue={value} fieldSize="sm" className="w-28">
        {/* the floor has nobody to inherit from, so it only says yes or no */}
        {!isDefault && <option value="inherit">{t("group.inherit")}</option>}
        <option value="allow">{t("group.allow")}</option>
        <option value="deny">{t("group.deny")}</option>
      </SelectInput>
    </label>
  );
};

const MemberEditor = ({ group, accountList }: { group: Group; accountList: Account[] }) => {
  const t = useT();
  const fetcher = useFetcher();
  const busy = fetcher.state !== "idle";
  const memberSet = new Set(group.memberIdList);

  return (
    <fetcher.Form method="post" className="flex flex-col gap-2">
      <input type="hidden" name="intent" value="member" />
      <input type="hidden" name="id" value={group.id} />
      <p className="text-xs text-ink-3">{t("group.member")}</p>
      <div className="flex max-h-44 flex-col gap-0.5 overflow-y-auto scroll-thin">
        {accountList.map((a) => (
          <label key={a.id} className="flex items-center gap-2 py-0.5 text-sm">
            <input
              type="checkbox"
              name="accountId"
              value={a.id}
              defaultChecked={memberSet.has(a.id)}
              className="size-3.5 accent-[var(--tone-brand)]"
            />
            <span className="truncate text-ink-2">{a.email}</span>
            {a.kind === "service" && <Badge tone="muted">{t("adminAccount.service")}</Badge>}
          </label>
        ))}
      </div>
      <Button variant="subtle" size="sm" pending={busy} className="self-start">
        {t("group.saveMember")}
      </Button>
    </fetcher.Form>
  );
};

export default function GroupList({ loaderData, actionData }: Route.ComponentProps) {
  const { groupList, accountList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <>
      <ShellHeader title={t("group.title")} />
      <ShellContent>
        {actionData && !actionData.ok && <ErrorBanner message={actionData.error} />}

        <Panel>
          <div className="border-b border-line px-4 py-2.5">
            <p className="text-sm font-medium text-ink">{t("group.howItWorks")}</p>
            <p className="text-xs text-ink-3">{t("group.howItWorksHint")}</p>
          </div>
          <Form method="post" className="flex gap-2 px-4 py-3">
            <input type="hidden" name="intent" value="create" />
            <TextInput
              name="name"
              required
              placeholder={t("group.namePlaceholder")}
              className="max-w-64 flex-1"
            />
            <Button disabled={busy}>{t("common.add")}</Button>
          </Form>
        </Panel>

        {groupList.length === 0 ? (
          <Panel>
            <EmptyText>{t("group.empty")}</EmptyText>
          </Panel>
        ) : (
          groupList.map((g, index) => (
            <Panel key={g.id}>
              <div className="flex items-center justify-between gap-2 border-b border-line px-4 py-2.5">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-medium text-ink">
                    {g.isDefault ? t("group.everyone") : g.name}
                  </p>
                  {g.isDefault ? (
                    <Badge tone="brand">{t("group.default")}</Badge>
                  ) : (
                    <Badge tone="muted">{t("group.memberCount", { count: g.memberIdList.length })}</Badge>
                  )}
                </div>
                {!g.isDefault && (
                  <div className="flex items-center gap-1">
                    <Form method="post">
                      <input type="hidden" name="intent" value="move" />
                      <input type="hidden" name="id" value={g.id} />
                      <input type="hidden" name="up" value="true" />
                      <IconButton
                        label={t("group.moveUp")}
                        size="iconSm"
                        variant="ghost"
                        disabled={busy || index === 0}
                      >
                        <ChevronUpIcon className="size-3.5" />
                      </IconButton>
                    </Form>
                    <Form method="post">
                      <input type="hidden" name="intent" value="move" />
                      <input type="hidden" name="id" value={g.id} />
                      <input type="hidden" name="up" value="false" />
                      <IconButton
                        label={t("group.moveDown")}
                        size="iconSm"
                        variant="ghost"
                        disabled={busy || index >= groupList.length - 2}
                      >
                        <ChevronDownIcon className="size-3.5" />
                      </IconButton>
                    </Form>
                    <Form method="post">
                      <input type="hidden" name="intent" value="delete" />
                      <input type="hidden" name="id" value={g.id} />
                      <IconButton
                        label={t("common.delete")}
                        size="iconSm"
                        variant="danger"
                        disabled={busy}
                        confirmMessage={t("group.confirmDelete")}
                      >
                        <TrashIcon className="size-3.5" />
                      </IconButton>
                    </Form>
                  </div>
                )}
              </div>

              <div className="grid gap-4 px-4 py-3 md:grid-cols-2">
                <Form method="post" className="flex flex-col gap-1">
                  <input type="hidden" name="intent" value="save" />
                  <input type="hidden" name="id" value={g.id} />
                  {g.isDefault ? (
                    <input type="hidden" name="name" value={g.name} />
                  ) : (
                    <label className="flex items-center justify-between gap-2 py-1">
                      <span className="text-sm text-ink-2">{t("group.name")}</span>
                      <TextInput name="name" defaultValue={g.name} fieldSize="sm" className="w-44" />
                    </label>
                  )}
                  <GrantSelect
                    name="canSend"
                    value={g.canSend}
                    label={t("permission.canSend")}
                    isDefault={g.isDefault}
                  />
                  <GrantSelect
                    name="canSendExternal"
                    value={g.canSendExternal}
                    label={t("permission.canSendExternal")}
                    isDefault={g.isDefault}
                  />
                  <GrantSelect
                    name="canReceiveExternal"
                    value={g.canReceiveExternal}
                    label={t("permission.canReceiveExternal")}
                    isDefault={g.isDefault}
                  />
                  <label className="flex items-center justify-between gap-2 py-1">
                    <span className="text-sm text-ink-2">{t("permission.dailyLimit")}</span>
                    <TextInput
                      name="dailySendLimit"
                      defaultValue={g.dailySendLimit ? String(g.dailySendLimit) : ""}
                      placeholder={t("permission.dailyLimitPlaceholder")}
                      fieldSize="sm"
                      className="w-28"
                    />
                  </label>
                  <Button variant="subtle" size="sm" disabled={busy} className="mt-1 self-start">
                    {t("common.save")}
                  </Button>
                </Form>

                {g.isDefault ? (
                  <p className="text-xs text-ink-faint">{t("group.everyoneHint")}</p>
                ) : (
                  <MemberEditor group={g} accountList={accountList} />
                )}
              </div>
            </Panel>
          ))
        )}
      </ShellContent>
    </>
  );
}
