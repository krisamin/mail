import { useRevalidator } from "react-router";
import type { Route } from "./+types/system";
import { apiFetch } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";
import { Badge, Button, Card, PageTitle } from "~/components";

// System check — self-dial port probes, DB latency, queue stats, uptime.

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
  queue: { ok: boolean; stats?: Record<string, number>; error?: string };
  port: PortCheck[];
  note: string;
};

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const status = await apiFetch<SystemStatus>(user.idToken, "/api/admin/system");
  return { status, checkedAt: new Date().toISOString() };
};

export default function System({ loaderData }: Route.ComponentProps) {
  const { status, checkedAt } = loaderData;
  const revalidator = useRevalidator();

  return (
    <div className="flex flex-col gap-6">
      <PageTitle
        title="시스템 점검"
        aside={
          <div className="flex items-center gap-3">
            <span className="text-xs text-text-2" suppressHydrationWarning>
              {checkedAt.replace("T", " ").slice(0, 19)}
            </span>
            <Button
              variant="outline"
              onClick={() => revalidator.revalidate()}
              disabled={revalidator.state !== "idle"}
            >
              다시 점검
            </Button>
          </div>
        }
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <Card>
          <div className="flex flex-col gap-1 px-4 py-3">
            <p className="text-xs text-text-2">가동 시간</p>
            <p className="text-lg font-semibold text-text-0">{status.uptime}</p>
            <p className="text-[11px] text-text-2">{status.hostname}</p>
          </div>
        </Card>
        <Card>
          <div className="flex flex-col gap-1 px-4 py-3">
            <div className="flex items-center justify-between">
              <p className="text-xs text-text-2">데이터베이스</p>
              <Badge tone={status.db.ok ? "ok" : "bad"}>{status.db.ok ? "정상" : "오류"}</Badge>
            </div>
            <p className="text-lg font-semibold text-text-0">{status.db.latency}</p>
            {status.db.error && <p className="text-[11px] text-bad">{status.db.error}</p>}
          </div>
        </Card>
        <Card>
          <div className="flex flex-col gap-1 px-4 py-3">
            <div className="flex items-center justify-between">
              <p className="text-xs text-text-2">발송 큐</p>
              <Badge tone={status.queue.ok ? "ok" : "bad"}>{status.queue.ok ? "정상" : "오류"}</Badge>
            </div>
            <p className="text-sm text-text-1">
              대기 {status.queue.stats?.pending ?? 0} · 완료 {status.queue.stats?.sent ?? 0} · 실패{" "}
              {status.queue.stats?.failed ?? 0}
            </p>
            {status.queue.error && <p className="text-[11px] text-bad">{status.queue.error}</p>}
          </div>
        </Card>
      </div>

      <Card>
        <div className="border-b border-line px-4 py-2.5">
          <p className="text-sm font-medium text-text-0">프로토콜 포트</p>
          <p className="text-[11px] text-text-2">{status.note}</p>
        </div>
        <ul className="divide-y divide-line">
          {status.port.map((p) => (
            <li key={p.name} className="flex items-center justify-between px-4 py-2.5">
              <div className="flex flex-col gap-0.5">
                <p className="text-sm text-text-0">
                  {p.name} <span className="text-text-2">{p.addr}</span>
                </p>
                {p.banner && <p className="truncate text-[11px] text-text-2">{p.banner}</p>}
                {p.error && <p className="text-[11px] text-bad">{p.error}</p>}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {p.latency && <span className="text-[11px] text-text-2">{p.latency}</span>}
                <Badge tone={p.open ? "ok" : "bad"}>{p.open ? "열림" : "닫힘"}</Badge>
              </div>
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}
