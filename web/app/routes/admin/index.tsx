import { Link } from "react-router";
import type { Route } from "./+types/index";
import { apiFetch, type Account, type Domain } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireAdmin } from "~/lib/session.server";
import { Badge, DomainIcon, EmptyState, Panel, PanelHeader, StatCard } from "~/kit";
import { ShellContent, ShellHeader } from "~/shell/app-shell";

// Dashboard — the four numbers an operator checks first, then the domains.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);
  const [domainList, accountList, queueStatMap] = await Promise.all([
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain").then((r) => r ?? []),
    apiFetch<Account[]>(user.idToken, "/api/admin/account").then((r) => r ?? []),
    apiFetch<Record<string, number>>(user.idToken, "/api/admin/queue/stat"),
  ]);
  return { domainList, accountCount: accountList.length, queueStatMap };
};

export default function AdminIndex({ loaderData }: Route.ComponentProps) {
  const { domainList, accountCount, queueStatMap } = loaderData;
  const t = useT();
  const activeDomainCount = domainList.filter((d) => d.active).length;

  return (
    <>
      <ShellHeader title={t("admin.dashboard")} />
      <ShellContent>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatCard label={t("admin.activeDomain")} value={activeDomainCount} tone="text-ok" />
          <StatCard label={t("admin.account")} value={accountCount} />
          <StatCard label={t("admin.queuePending")} value={queueStatMap.pending ?? 0} tone="text-warn" />
          <StatCard label={t("admin.queueFailed")} value={queueStatMap.failed ?? 0} tone="text-bad" />
        </div>

        <Panel>
          <PanelHeader
            title={t("admin.domain")}
            action={
              <Link to="/admin/domain" className="text-xs text-brand hover:underline">
                {t("admin.manage")}
              </Link>
            }
          />
          {domainList.length === 0 ? (
            <EmptyState
              icon={<DomainIcon className="size-7" strokeWidth={1.5} />}
              title={t("admin.noDomain")}
            />
          ) : (
            <ul className="divide-y divide-line">
              {domainList.map((domain) => (
                <li key={domain.id} className="flex items-center justify-between gap-3 px-4 py-2.5">
                  <span className="truncate text-sm text-ink">{domain.name}</span>
                  <div className="flex items-center gap-2">
                    {domain.dkimSelector && <Badge tone="brand">DKIM</Badge>}
                    <Badge tone={domain.active ? "ok" : "muted"}>
                      {domain.active ? t("common.active") : t("common.inactive")}
                    </Badge>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </ShellContent>
    </>
  );
}
