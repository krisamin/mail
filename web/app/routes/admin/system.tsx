import { useRevalidator } from "react-router";
import type { Route } from "./+types/system";
import { apiFetch } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";
import { Badge, Button, Card, PageTitle } from "~/components";

// System check — internal listeners (self-dial), external reachability
// (public hostname + standard ports), DB latency, queue stats, uptime.

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
  listener: PortCheck[];
  external: PortCheck[];
  externalHost: string;
  note: string;
};

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const status = await apiFetch<SystemStatus>(user.idToken, "/api/admin/system");
  return { status, checkedAt: new Date().toISOString() };
};

const PortRowList = ({ list, okLabel, badLabel }: { list: PortCheck[]; okLabel: string; badLabel: string }) => (
  <ul className="divide-y divide-line">
    {list.map((p) => (
      <li key={p.name + p.addr} className="flex items-center justify-between px-4 py-2.5">
        <div className="flex min-w-0 flex-col gap-0.5">
          <p className="text-sm text-text-0">
            {p.name} <span className="text-text-2">{p.addr}</span>
          </p>
          {p.banner && <p className="truncate text-[11px] text-text-2">{p.banner}</p>}
          {p.error && <p className="truncate text-[11px] text-bad" title={p.error}>{p.error}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {p.latency && <span className="text-[11px] text-text-2">{p.latency}</span>}
          <Badge tone={p.open ? "ok" : "bad"}>{p.open ? okLabel : badLabel}</Badge>
        </div>
      </li>
    ))}
  </ul>
);

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
          <p className="text-sm font-medium text-text-0">외부 도달성 — {status.externalHost}</p>
          <p className="text-[11px] text-text-2">
            공인 호스트네임의 표준 포트로 실접속 — 클라이언트(Thunderbird 등)가 겪는 경로.
            LB·라우터 포워딩이 뚫려야 성공. 헤어핀 NAT 미지원 라우터에선 오탐 가능.
          </p>
        </div>
        <PortRowList list={status.external} okLabel="도달" badLabel="차단" />
      </Card>

      <Card>
        <div className="border-b border-line px-4 py-2.5">
          <p className="text-sm font-medium text-text-0">내부 리스너</p>
          <p className="text-[11px] text-text-2">
            데몬 자기 점검(self-dial) — 프로세스가 listen 중이고 프로토콜 응답이 정상인지만 확인.
            외부 접속 가능 여부와는 별개.
          </p>
        </div>
        <PortRowList list={status.listener} okLabel="정상" badLabel="다운" />
      </Card>
    </div>
  );
}
