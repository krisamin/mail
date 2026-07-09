import { Link } from "react-router";
import type { Route } from "./+types/index";
import { apiFetch, type Domain } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const [domainList, queueStats] = await Promise.all([
    apiFetch<Domain[]>(user.idToken, "/api/admin/domain"),
    apiFetch<Record<string, number>>(user.idToken, "/api/admin/queue/stats"),
  ]);
  return { domainList: domainList ?? [], queueStats };
};

const StatCard = ({ label, value, tone }: { label: string; value: number; tone?: string }) => (
  <div className="rounded-md border border-line bg-bg-1 p-4">
    <p className="text-xs text-text-2">{label}</p>
    <p className={`mt-1 text-2xl font-bold ${tone ?? "text-text-0"}`}>{value}</p>
  </div>
);

export default function AdminIndex({ loaderData }: Route.ComponentProps) {
  const { domainList, queueStats } = loaderData;
  const activeDomains = domainList.filter((d) => d.active).length;
  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-xl font-bold">대시보드</h1>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard label="도메인" value={domainList.length} />
        <StatCard label="활성 도메인" value={activeDomains} tone="text-ok" />
        <StatCard label="발송 대기" value={queueStats.pending ?? 0} tone="text-warn" />
        <StatCard label="발송 실패" value={queueStats.failed ?? 0} tone="text-bad" />
      </div>

      <div className="rounded-md border border-line bg-bg-1">
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <h2 className="text-sm font-medium">도메인</h2>
          <Link to="/admin/domain" className="text-xs text-accent hover:text-accent-hover">
            관리 →
          </Link>
        </div>
        {domainList.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-text-2">도메인 없음</p>
        ) : (
          <ul className="divide-y divide-line">
            {domainList.map((d) => (
              <li key={d.id} className="flex items-center justify-between px-4 py-2.5">
                <Link
                  to={`/admin/domain/${d.id}/address`}
                  className="text-sm text-text-0 hover:text-accent"
                >
                  {d.name}
                </Link>
                <div className="flex items-center gap-2">
                  {d.dkimSelector && (
                    <span className="rounded bg-accent-soft px-1.5 py-0.5 text-[10px] text-accent">
                      DKIM
                    </span>
                  )}
                  <span
                    className={`rounded px-1.5 py-0.5 text-[10px] ${
                      d.active ? "bg-ok/20 text-ok" : "bg-bg-3 text-muted"
                    }`}
                  >
                    {d.active ? "활성" : "비활성"}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
