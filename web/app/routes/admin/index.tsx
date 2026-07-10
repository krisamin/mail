import { Link } from "react-router";
import type { Route } from "./+types/index";
import { apiFetch, type Account, type Domain } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireAdmin } from "~/lib/session.server";
import { Badge, Card, EmptyText, PageTitle, StatCard } from "~/components";

// Admin dashboard — high-level stat cards and quick links.

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
    <div className="flex flex-col gap-6">
      <PageTitle title={t("dashboard.title")} />

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard label={t("dashboard.activeDomain")} value={activeDomainCount} tone="text-ok" />
        <StatCard label={t("dashboard.account")} value={accountCount} />
        <StatCard label={t("dashboard.queuePending")} value={queueStatMap.pending ?? 0} tone="text-warn" />
        <StatCard label={t("dashboard.queueFailed")} value={queueStatMap.failed ?? 0} tone="text-bad" />
      </div>

      <Card>
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-medium">{t("dashboard.domain")}</h2>
          <Link to="/admin/domain" className="text-xs text-accent hover:text-accent-hover">
            {t("dashboard.manage")}
          </Link>
        </div>
        {domainList.length === 0 ? (
          <EmptyText>{t("dashboard.noDomain")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {domainList.map((d) => (
              <li key={d.id} className="flex items-center justify-between px-4 py-2.5">
                <span className="text-sm text-text-0">{d.name}</span>
                <div className="flex items-center gap-2">
                  {d.dkimSelector && <Badge tone="accent">DKIM</Badge>}
                  <Badge tone={d.active ? "ok" : "muted"}>
                    {d.active ? t("common.active") : t("common.inactive")}
                  </Badge>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
