import { Form, useNavigation } from "react-router";
import type { Route } from "./+types/relay";
import { ApiError, apiFetch, type Domain, type Relay } from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { getLocale } from "~/lib/locale.server";
import { requireUser } from "~/lib/session.server";
import {
  Badge,
  Button,
  Card,
  CheckboxLabel,
  EmptyText,
  ErrorBanner,
  PageTitle,
  SelectInput,
  TextInput,
} from "~/components";

// Outbound relay management — relays live in the DB (no env restarts).
// Passwords are write-only: the server never returns them (hasPassword flag only).

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  const [relayList, domainList] = await Promise.all([
    apiFetch<Relay[]>(user.idToken, "/api/admin/relay"),
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain"),
  ]);
  return { relayList: relayList ?? [], domainList: domainList ?? [] };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireUser(request);
  const form = await request.formData();
  const intent = form.get("intent");

  try {
    switch (intent) {
      case "create": {
        await apiFetch(user.idToken, "/api/admin/relay", {
          method: "POST",
          body: {
            name: String(form.get("name") ?? ""),
            host: String(form.get("host") ?? ""),
            port: Number(form.get("port") ?? 587),
            username: String(form.get("username") ?? ""),
            password: String(form.get("password") ?? ""),
            starttls: form.get("starttls") === "on",
            isDefault: form.get("isDefault") === "on",
          },
        });
        return { ok: true as const };
      }
      case "update": {
        await apiFetch(user.idToken, `/api/admin/relay/${form.get("id")}`, {
          method: "PUT",
          body: {
            name: String(form.get("name") ?? ""),
            host: String(form.get("host") ?? ""),
            port: Number(form.get("port") ?? 587),
            username: String(form.get("username") ?? ""),
            // empty string = keep existing password
            password: String(form.get("password") ?? ""),
            starttls: form.get("starttls") === "on",
            isDefault: form.get("isDefault") === "on",
            active: form.get("active") === "on",
          },
        });
        return { ok: true as const };
      }
      case "delete": {
        await apiFetch(user.idToken, `/api/admin/relay/${form.get("id")}`, {
          method: "DELETE",
        });
        return { ok: true as const };
      }
      case "assign": {
        const relayIdRaw = String(form.get("relayId") ?? "");
        await apiFetch(user.idToken, `/api/admin/domain/${form.get("domainId")}/relay`, {
          method: "PUT",
          body: { relayId: relayIdRaw === "" ? null : Number(relayIdRaw) },
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

export default function RelayList({ loaderData, actionData }: Route.ComponentProps) {
  const { relayList, domainList } = loaderData;
  const t = useT();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <PageTitle title={t("relay.title")} description={t("relay.description")} />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      {/* New relay */}
      <Form method="post">
        <Card className="flex flex-col gap-2 p-4">
          <input type="hidden" name="intent" value="create" />
          <p className="text-sm font-medium">{t("relay.new")}</p>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <TextInput name="name" required placeholder={t("relay.namePlaceholder")} />
            <TextInput name="host" required placeholder="smtp.resend.com" />
            <TextInput name="port" type="number" defaultValue={587} />
            <TextInput name="username" placeholder="username" />
            <TextInput
              name="password"
              type="password"
              placeholder={t("relay.passwordPlaceholder")}
              className="col-span-2"
            />
            <CheckboxLabel name="starttls" defaultChecked label="STARTTLS" />
            <CheckboxLabel name="isDefault" label={t("relay.defaultRelay")} />
          </div>
          <Button disabled={busy} className="self-start">
            {t("common.add")}
          </Button>
        </Card>
      </Form>

      {/* Relay list */}
      <Card>
        {relayList.length === 0 ? (
          <EmptyText>{t("relay.empty")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {relayList.map((r) => (
              <li key={r.id} className="px-4 py-3">
                <Form method="post" className="flex flex-col gap-2">
                  <input type="hidden" name="intent" value="update" />
                  <input type="hidden" name="id" value={r.id} />
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium">{r.name}</span>
                    {r.isDefault && <Badge tone="accent">{t("relay.default")}</Badge>}
                    {!r.active && <Badge>{t("common.inactive")}</Badge>}
                    <span className="text-xs text-text-2">
                      {r.host}:{r.port}
                    </span>
                  </div>
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                    <TextInput name="name" defaultValue={r.name} />
                    <TextInput name="host" defaultValue={r.host} />
                    <TextInput name="port" type="number" defaultValue={r.port} />
                    <TextInput name="username" defaultValue={r.username} />
                    <TextInput
                      name="password"
                      type="password"
                      placeholder={r.hasPassword ? t("relay.passwordKeep") : t("relay.passwordPlaceholder")}
                      className="col-span-2"
                    />
                    <CheckboxLabel name="starttls" defaultChecked={r.starttls} label="STARTTLS" />
                    <CheckboxLabel name="isDefault" defaultChecked={r.isDefault} label={t("relay.default")} />
                    <CheckboxLabel name="active" defaultChecked={r.active} label={t("common.active")} />
                  </div>
                  <div className="flex gap-3">
                    <Button variant="link" disabled={busy}>
                      {t("common.save")}
                    </Button>
                  </div>
                </Form>
                <Form method="post" className="mt-1">
                  <input type="hidden" name="intent" value="delete" />
                  <input type="hidden" name="id" value={r.id} />
                  <Button variant="linkDanger" disabled={busy}>
                    {t("common.delete")}
                  </Button>
                </Form>
              </li>
            ))}
          </ul>
        )}
      </Card>

      {/* Per-domain relay assignment */}
      <Card className="p-4">
        <p className="mb-3 text-sm font-medium">{t("relay.perDomain")}</p>
        <ul className="flex flex-col gap-2">
          {domainList.map((d) => (
            <li key={d.id}>
              <Form method="post" className="flex items-center gap-2">
                <input type="hidden" name="intent" value="assign" />
                <input type="hidden" name="domainId" value={d.id} />
                <span className="w-40 text-sm">{d.name}</span>
                <SelectInput name="relayId" defaultValue={d.relayId ?? ""} fieldSize="sm" className="py-1">
                  <option value="">{t("relay.defaultOption")}</option>
                  {relayList.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.name} — {r.host}
                    </option>
                  ))}
                </SelectInput>
                <Button variant="link" disabled={busy}>
                  {t("common.assign")}
                </Button>
              </Form>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}
