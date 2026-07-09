import { Form, useNavigation, useSearchParams } from "react-router";
import type { Route } from "./+types/queue";
import { ApiError, apiFetch, type QueueItem } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";
import { Badge, Button, Card, EmptyText, ErrorBanner, PageTitle, type BadgeTone } from "~/components";

// Outbound queue — filter by status, retry failed items.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = (await getUser(request))!;
  const url = new URL(request.url);
  const status = url.searchParams.get("status") ?? "";

  const [items, stats] = await Promise.all([
    apiFetch<QueueItem[]>(user.idToken, `/api/admin/queue?status=${status}`),
    apiFetch<Record<string, number>>(user.idToken, "/api/admin/queue/stats"),
  ]);
  return { items: items ?? [], stats, status };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = (await getUser(request))!;
  const form = await request.formData();
  try {
    await apiFetch(user.idToken, `/api/admin/queue/${form.get("id")}/retry`, { method: "POST" });
    return { ok: true as const };
  } catch (e) {
    if (e instanceof ApiError) return { ok: false as const, error: e.message };
    throw e;
  }
};

const statusTone: Record<string, BadgeTone> = {
  pending: "warn",
  sent: "ok",
  failed: "bad",
};

const filters = [
  { value: "", label: "전체" },
  { value: "pending", label: "대기" },
  { value: "sent", label: "완료" },
  { value: "failed", label: "실패" },
];

export default function Queue({ loaderData, actionData }: Route.ComponentProps) {
  const { items, stats, status } = loaderData;
  const [, setSearchParams] = useSearchParams();
  const nav = useNavigation();
  const busy = nav.state !== "idle";

  return (
    <div className="flex flex-col gap-6">
      <PageTitle
        title="발송 큐"
        aside={
          <p className="text-xs text-text-2">
            대기 {stats.pending ?? 0} · 완료 {stats.sent ?? 0} · 실패 {stats.failed ?? 0}
          </p>
        }
      />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      <div className="flex gap-1">
        {filters.map((f) => (
          <button
            key={f.value}
            type="button"
            onClick={() => setSearchParams(f.value ? { status: f.value } : {})}
            className={`rounded-md px-3 py-1.5 text-xs ${
              status === f.value ? "bg-bg-3 text-text-0" : "text-text-2 hover:bg-bg-2"
            }`}
          >
            {f.label}
          </button>
        ))}
      </div>

      <Card>
        {items.length === 0 ? (
          <EmptyText>항목 없음</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {items.map((m) => (
              <li key={m.id} className="flex flex-col gap-1 px-4 py-2.5">
                <div className="flex items-center justify-between">
                  <p className="truncate text-sm">
                    <span className="text-text-2">{m.from}</span>
                    <span className="mx-1.5 text-muted">→</span>
                    <span className="text-text-0">{m.rcpt}</span>
                  </p>
                  <div className="flex shrink-0 items-center gap-2">
                    <Badge tone={statusTone[m.status] ?? "muted"}>{m.status}</Badge>
                    {m.status === "failed" && (
                      <Form method="post">
                        <input type="hidden" name="id" value={m.id} />
                        <Button variant="link" disabled={busy}>
                          재시도
                        </Button>
                      </Form>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-3 text-[11px] text-text-2">
                  <span>시도 {m.attemptCount}회</span>
                  <span suppressHydrationWarning>
                    {m.createdAt.replace("T", " ").replace("Z", "")}
                  </span>
                  {m.lastError && (
                    <span className="truncate text-bad" title={m.lastError}>
                      {m.lastError}
                    </span>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}
