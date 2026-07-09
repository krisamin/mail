import { Link } from "react-router";
import type { Route } from "./+types/index";
import { apiFetch, type Account, type Domain } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";
import { Badge, Card, EmptyText, PageTitle, StatCard } from "~/components";

// Admin dashboard — high-level stats and quick links.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const [domainList, accountList, queueStats] = await Promise.all([
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain").then((r) => r ?? []),
    apiFetch<Account[]>(user.idToken, "/api/admin/account").then((r) => r ?? []),
    apiFetch<Record<string, number>>(user.idToken, "/api/admin/queue/stats"),
  ]);
  return { domainList, accountCount: accountList.length, queueStats };
};

export default function AdminIndex({ loaderData }: Route.ComponentProps) {
  const { domainList, accountCount, queueStats } = loaderData;
  const activeDomains = domainList.filter((d) => d.active).length;
  return (
    <div className="flex flex-col gap-6">
      <PageTitle title="대시보드" />

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard label="활성 도메인" value={activeDomains} tone="text-ok" />
        <StatCard label="계정" value={accountCount} />
        <StatCard label="발송 대기" value={queueStats.pending ?? 0} tone="text-warn" />
        <StatCard label="발송 실패" value={queueStats.failed ?? 0} tone="text-bad" />
      </div>

      <Card>
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-medium">도메인</h2>
          <Link to="/admin/domain" className="text-xs text-accent hover:text-accent-hover">
            관리 →
          </Link>
        </div>
        {domainList.length === 0 ? (
          <EmptyText>도메인 없음</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {domainList.map((d) => (
              <li key={d.id} className="flex items-center justify-between px-4 py-2.5">
                <span className="text-sm text-text-0">{d.name}</span>
                <div className="flex items-center gap-2">
                  {d.dkimSelector && <Badge tone="accent">DKIM</Badge>}
                  <Badge tone={d.active ? "ok" : "muted"}>{d.active ? "활성" : "비활성"}</Badge>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
