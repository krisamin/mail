import { useEffect } from "react";
import { Form, useFetcher, useNavigation, useRevalidator } from "react-router";
import type { Route } from "./+types/system";
import { ApiError, apiFetch } from "~/lib/api.server";
import { translate } from "~/i18n";
import { useT } from "~/lib/i18n";
import { ShellContent, ShellHeader } from "~/shell/app-shell";
import { LOCALE_LABEL_MAP, LOCALE_LIST } from "~/lib/locale";
import { isLocaleSetting, primeLocaleSetting } from "~/lib/locale.server";
import { getLocale } from "~/lib/locale.server";
import { requireAdmin } from "~/lib/session.server";
import { Badge, Banner, Button, Panel, ErrorBanner, SelectInput, TimeText } from "~/kit";

// System check — fast page load: listener/DB/queue only.
// External reachability is slow (blocked port = dial timeout) so it loads
// asynchronously via fetcher after render — no more navbar freeze.

type PortCheck = {
  name: string;
  addr: string;
  open: boolean;
  banner?: string;
  latency?: string;
  error?: string;
};

type SystemStatus = {
  uptime: string;
  hostname: string;
  db: { ok: boolean; latency: string; error?: string };
  queue: { ok: boolean; statMap?: Record<string, number>; error?: string };
  listener: PortCheck[];
  externalHost: string;
  note: string;
};

type ExternalStatus = {
  externalHost: string;
  external: PortCheck[];
  note: string;
};

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);
  const url = new URL(request.url);

  // Fetcher path: ?external=1 runs only the slow external reachability check.
  if (url.searchParams.get("external") === "1") {
    const external = await apiFetch<ExternalStatus>(user.idToken, "/api/admin/system/external", {
      timeoutMs: 30_000, // slow port probes — dial timeouts add up
    });
    return { kind: "external" as const, external };
  }

  const status = await apiFetch<SystemStatus>(user.idToken, "/api/admin/system");
  const localeSetting = await apiFetch<{ locale: string }>(user.idToken, "/api/setting/locale");
  return {
    kind: "status" as const,
    status,
    localeSetting: localeSetting.locale,
    checkedAt: new Date().toISOString(),
  };
};

// Save the global display language (admin only; applies to every visitor).
export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireAdmin(request);
  const form = await request.formData();
  const locale = String(form.get("locale") ?? "");
  if (!isLocaleSetting(locale))
    return {
      ok: false as const,
      error: translate(await getLocale(request), "common.invalidValue"),
    };
  try {
    await apiFetch(user.idToken, "/api/admin/setting/locale", {
      method: "PUT",
      body: { locale },
    });
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
  primeLocaleSetting(locale); // refresh the SSR cache so the change is instant
  return { ok: true as const };
};

const PortRowList = ({ list, okLabel, badLabel }: { list: PortCheck[]; okLabel: string; badLabel: string }) => (
  <ul className="divide-y divide-line">
    {list.map((p) => (
      <li key={p.name + p.addr} className="flex items-center justify-between px-4 py-2.5">
        <div className="flex min-w-0 flex-col gap-0.5">
          <p className="text-sm text-ink">
            {p.name} <span className="text-ink-3">{p.addr}</span>
          </p>
          {p.banner && <p className="truncate text-[11px] text-ink-3">{p.banner}</p>}
          {p.error && <p className="truncate text-[11px] text-bad" title={p.error}>{p.error}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {p.latency && <span className="text-[11px] text-ink-3">{p.latency}</span>}
          <Badge tone={p.open ? "ok" : "bad"}>{p.open ? okLabel : badLabel}</Badge>
        </div>
      </li>
    ))}
  </ul>
);

export default function System({ loaderData, actionData }: Route.ComponentProps) {
  const t = useT();
  const revalidator = useRevalidator();
  const externalFetcher = useFetcher<typeof loader>();
  const nav = useNavigation();

  // Kick off the external check after render — the page paints immediately,
  // the slow part fills in on its own.
  useEffect(() => {
    if (externalFetcher.state === "idle" && !externalFetcher.data) {
      externalFetcher.load("/admin/system?external=1");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (loaderData.kind !== "status") return null; // fetcher responses never render directly
  const { status, localeSetting, checkedAt } = loaderData;

  const externalData =
    externalFetcher.data?.kind === "external" ? externalFetcher.data.external : null;
  const externalLoading = !externalData;

  const recheck = () => {
    revalidator.revalidate();
    externalFetcher.load("/admin/system?external=1");
  };

  return (
    <>
      <ShellHeader
        title={t("system.title")}
        action={
          <Button
            variant="outline"
            size="sm"
            onClick={recheck}
            disabled={revalidator.state !== "idle" || externalFetcher.state !== "idle"}
          >
            {t("system.recheck")}
          </Button>
        }
      />
      <ShellContent>
      <div className="grid gap-4 sm:grid-cols-3">
        <Panel>
          <div className="flex flex-col gap-1 px-4 py-3">
            <p className="text-xs text-ink-3">{t("system.uptime")}</p>
            <p className="text-lg font-semibold text-ink">{status.uptime}</p>
            <p className="text-[11px] text-ink-3">{status.hostname}</p>
          </div>
        </Panel>
        <Panel>
          <div className="flex flex-col gap-1 px-4 py-3">
            <div className="flex items-center justify-between">
              <p className="text-xs text-ink-3">{t("system.db")}</p>
              <Badge tone={status.db.ok ? "ok" : "bad"}>
                {status.db.ok ? t("common.ok") : t("common.error")}
              </Badge>
            </div>
            <p className="text-lg font-semibold text-ink">{status.db.latency}</p>
            {status.db.error && <p className="text-[11px] text-bad">{status.db.error}</p>}
          </div>
        </Panel>
        <Panel>
          <div className="flex flex-col gap-1 px-4 py-3">
            <div className="flex items-center justify-between">
              <p className="text-xs text-ink-3">{t("system.queue")}</p>
              <Badge tone={status.queue.ok ? "ok" : "bad"}>
                {status.queue.ok ? t("common.ok") : t("common.error")}
              </Badge>
            </div>
            <p className="text-sm text-ink-2">
              {t("queue.stat", {
                pending: status.queue.statMap?.pending ?? 0,
                sent: status.queue.statMap?.sent ?? 0,
                failed: status.queue.statMap?.failed ?? 0,
              })}
            </p>
            {status.queue.error && <p className="text-[11px] text-bad">{status.queue.error}</p>}
          </div>
        </Panel>
      </div>

      <Panel>
        <div className="border-b border-line px-4 py-2.5">
          <p className="text-sm font-medium text-ink">
            {t("system.externalPrefix")} {status.externalHost}
          </p>
          <p className="text-[11px] text-ink-3">{t("system.externalDesc")}</p>
        </div>
        {externalLoading ? (
          <p className="px-4 py-6 text-center text-xs text-ink-3">{t("system.checking")}</p>
        ) : (
          <PortRowList
            list={externalData.external}
            okLabel={t("system.reachable")}
            badLabel={t("system.blocked")}
          />
        )}
      </Panel>

      <Panel>
        <div className="border-b border-line px-4 py-2.5">
          <p className="text-sm font-medium text-ink">{t("system.listener")}</p>
          <p className="text-[11px] text-ink-3">{t("system.listenerDesc")}</p>
        </div>
        <PortRowList list={status.listener} okLabel={t("system.up")} badLabel={t("system.down")} />
      </Panel>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium text-ink-2">{t("system.setting")}</h2>
        <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />
        {actionData?.ok && <Banner>{t("system.localeSaved")}</Banner>}
        <Panel className="p-4">
          <p className="text-sm font-medium text-ink">{t("system.locale")}</p>
          <p className="mt-1 text-[11px] text-ink-3">{t("system.localeDesc")}</p>
          <Form method="post" className="mt-3 flex gap-2">
            <SelectInput name="locale" defaultValue={localeSetting} className="flex-1 sm:max-w-xs">
              <option value="auto">{t("system.localeAuto")}</option>
              {LOCALE_LIST.map((l) => (
                <option key={l} value={l}>
                  {LOCALE_LABEL_MAP[l]}
                </option>
              ))}
            </SelectInput>
            <Button disabled={nav.state !== "idle"}>{t("common.save")}</Button>
          </Form>
        </Panel>
      </section>
      </ShellContent>
    </>
  );
}
