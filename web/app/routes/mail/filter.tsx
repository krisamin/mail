import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/filter";
import { ApiError, apiFetch, type FilterRule } from "~/lib/api.server";
import { useT, type TFunc } from "~/lib/i18n";
import { requireUser } from "~/lib/session.server";
import {
  Badge,
  Button,
  Card,
  EmptyText,
  ErrorBanner,
  PageTitle,
  SelectInput,
  TextInput,
} from "~/components";

// Filter rules — ordered list with inline create, toggle, reorder, delete.
// Rules apply on delivery (first match wins); see the Go delivery path.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const ruleList = (await apiFetch<FilterRule[]>(user.idToken, "/api/me/filter")) ?? [];
  return { ruleList };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create": {
        await apiFetch(user.idToken, "/api/me/filter", {
          method: "POST",
          body: {
            name: String(form.get("name") ?? ""),
            field: String(form.get("field") ?? ""),
            headerName: String(form.get("headerName") ?? ""),
            matchType: String(form.get("matchType") ?? ""),
            pattern: String(form.get("pattern") ?? ""),
            action: String(form.get("action") ?? ""),
            actionMailbox: String(form.get("actionMailbox") ?? ""),
          },
        });
        return { ok: true as const };
      }
      case "toggle": {
        // round-trip the full rule — the API PUT rewrites the row
        await apiFetch(user.idToken, `/api/me/filter/${form.get("id")}`, {
          method: "PUT",
          body: {
            name: String(form.get("name") ?? ""),
            active: form.get("active") === "true",
            field: String(form.get("field") ?? ""),
            headerName: String(form.get("headerName") ?? ""),
            matchType: String(form.get("matchType") ?? ""),
            pattern: String(form.get("pattern") ?? ""),
            action: String(form.get("action") ?? ""),
            actionMailbox: String(form.get("actionMailbox") ?? ""),
          },
        });
        return { ok: true as const };
      }
      case "move": {
        await apiFetch(user.idToken, `/api/me/filter/${form.get("id")}/move`, {
          method: "POST",
          body: { direction: Number(form.get("direction")) },
        });
        return { ok: true as const };
      }
      case "delete": {
        await apiFetch(user.idToken, `/api/me/filter/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      default:
        return { ok: false as const, error: "unknown intent" };
    }
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

const fieldLabel = (t: TFunc, field: string): string => {
  switch (field) {
    case "from":
      return t("filter.fieldFrom");
    case "to":
      return t("filter.fieldTo");
    case "subject":
      return t("filter.fieldSubject");
    case "header":
      return t("filter.fieldHeader");
    default:
      return field;
  }
};

const matchLabel = (t: TFunc, match: string): string => {
  switch (match) {
    case "contains":
      return t("filter.matchContains");
    case "equals":
      return t("filter.matchEquals");
    case "prefix":
      return t("filter.matchPrefix");
    case "suffix":
      return t("filter.matchSuffix");
    default:
      return match;
  }
};

const actionLabel = (t: TFunc, action: string): string => {
  switch (action) {
    case "move":
      return t("filter.actionMove");
    case "markSeen":
      return t("filter.actionMarkSeen");
    case "flag":
      return t("filter.actionFlag");
    case "discard":
      return t("filter.actionDiscard");
    default:
      return action;
  }
};

/** Hidden inputs carrying the rule's full state (PUT round-trip). */
const RuleStateInputs = ({ rule, activeOverride }: { rule: FilterRule; activeOverride?: boolean }) => (
  <>
    <input type="hidden" name="id" value={rule.id} />
    <input type="hidden" name="name" value={rule.name} />
    <input type="hidden" name="active" value={String(activeOverride ?? rule.active)} />
    <input type="hidden" name="field" value={rule.field} />
    <input type="hidden" name="headerName" value={rule.headerName} />
    <input type="hidden" name="matchType" value={rule.matchType} />
    <input type="hidden" name="pattern" value={rule.pattern} />
    <input type="hidden" name="action" value={rule.action} />
    <input type="hidden" name="actionMailbox" value={rule.actionMailbox} />
  </>
);

export default function FilterPage({ loaderData, actionData }: Route.ComponentProps) {
  const { ruleList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-4">
      <PageTitle title={t("filter.title")} description={t("filter.description")} />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      <Card>
        {ruleList.length === 0 ? (
          <EmptyText>{t("filter.empty")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {ruleList.map((rule, i) => (
              <li key={rule.id} className="flex items-center gap-3 px-4 py-2.5">
                <div className="flex flex-col gap-0.5">
                  <Form method="post">
                    <input type="hidden" name="intent" value="move" />
                    <input type="hidden" name="id" value={rule.id} />
                    <input type="hidden" name="direction" value="-1" />
                    <button
                      type="submit"
                      disabled={busy || i === 0}
                      aria-label={t("filter.up")}
                      className="text-xs text-text-2 hover:text-text-0 disabled:opacity-30"
                    >
                      ▲
                    </button>
                  </Form>
                  <Form method="post">
                    <input type="hidden" name="intent" value="move" />
                    <input type="hidden" name="id" value={rule.id} />
                    <input type="hidden" name="direction" value="1" />
                    <button
                      type="submit"
                      disabled={busy || i === ruleList.length - 1}
                      aria-label={t("filter.down")}
                      className="text-xs text-text-2 hover:text-text-0 disabled:opacity-30"
                    >
                      ▼
                    </button>
                  </Form>
                </div>
                <div className="min-w-0 flex-1">
                  <p className={`text-sm ${rule.active ? "text-text-0" : "text-muted line-through"}`}>
                    {rule.name}
                  </p>
                  <p className="text-xs text-text-2">
                    {fieldLabel(t, rule.field)}
                    {rule.field === "header" && ` (${rule.headerName})`}{" "}
                    {matchLabel(t, rule.matchType)}{" "}
                    <span className="font-mono text-text-1">"{rule.pattern}"</span>
                    <span className="mx-1.5 text-muted">→</span>
                    {actionLabel(t, rule.action)}
                    {rule.action === "move" && (
                      <span className="font-mono text-text-1"> {rule.actionMailbox}</span>
                    )}
                  </p>
                </div>
                {rule.action === "discard" && <Badge tone="bad">!</Badge>}
                <Form method="post">
                  <input type="hidden" name="intent" value="toggle" />
                  <RuleStateInputs rule={rule} activeOverride={!rule.active} />
                  <button
                    type="submit"
                    disabled={busy}
                    aria-pressed={rule.active}
                    className={`rounded px-2 py-1 text-xs transition-colors duration-100 ${
                      rule.active
                        ? "bg-ok/20 text-ok hover:bg-ok/30"
                        : "bg-bg-3 text-muted hover:bg-bg-2"
                    }`}
                  >
                    {rule.active ? t("common.active") : t("common.inactive")}
                  </button>
                </Form>
                <Form method="post">
                  <input type="hidden" name="intent" value="delete" />
                  <input type="hidden" name="id" value={rule.id} />
                  <Button variant="linkDanger" pending={busy} confirmMessage={t("filter.confirmDelete")}>
                    {t("common.delete")}
                  </Button>
                </Form>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card className="p-4">
        <h2 className="text-sm font-medium text-text-1">{t("filter.new")}</h2>
        <p className="mt-1 text-xs text-text-2">{t("filter.discardWarn")}</p>
        <Form method="post" className="mt-3 flex flex-col gap-2">
          <input type="hidden" name="intent" value="create" />
          <div className="flex flex-wrap gap-2">
            <TextInput
              name="name"
              required
              placeholder={t("filter.namePlaceholder")}
              className="flex-1"
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SelectInput name="field" defaultValue="from">
              <option value="from">{t("filter.fieldFrom")}</option>
              <option value="to">{t("filter.fieldTo")}</option>
              <option value="subject">{t("filter.fieldSubject")}</option>
              <option value="header">{t("filter.fieldHeader")}</option>
            </SelectInput>
            <TextInput name="headerName" placeholder={t("filter.headerPlaceholder")} />
            <SelectInput name="matchType" defaultValue="contains">
              <option value="contains">{t("filter.matchContains")}</option>
              <option value="equals">{t("filter.matchEquals")}</option>
              <option value="prefix">{t("filter.matchPrefix")}</option>
              <option value="suffix">{t("filter.matchSuffix")}</option>
            </SelectInput>
            <TextInput
              name="pattern"
              required
              placeholder={t("filter.patternPlaceholder")}
              className="flex-1"
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <SelectInput name="action" defaultValue="move">
              <option value="move">{t("filter.actionMove")}</option>
              <option value="markSeen">{t("filter.actionMarkSeen")}</option>
              <option value="flag">{t("filter.actionFlag")}</option>
              <option value="discard">{t("filter.actionDiscard")}</option>
            </SelectInput>
            <TextInput name="actionMailbox" placeholder={t("filter.mailboxPlaceholder")} className="flex-1" />
            <Button pending={busy}>{t("common.add")}</Button>
          </div>
        </Form>
      </Card>
    </div>
  );
}
