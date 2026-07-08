import { Form, useNavigation, useSearchParams } from "react-router";
import type { Route } from "./+types/queue";
import { ApiError, apiFetch, type QueueItem } from "~/lib/api.server";
import { getUser } from "~/lib/session.server";

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

const statusTone: Record<string, string> = {
  pending: "bg-warn/20 text-warn",
  sent: "bg-ok/20 text-ok",
  failed: "bg-bad/20 text-bad",
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
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">발송 큐</h1>
        <p className="text-xs text-text-2">
          대기 {stats.pending ?? 0} · 완료 {stats.sent ?? 0} · 실패 {stats.failed ?? 0}
        </p>
      </div>

      {actionData && !actionData.ok && (
        <p className="rounded-md border border-bad/40 bg-bad/10 px-3 py-2 text-sm text-bad">
          {actionData.error}
        </p>
      )}

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

      <div className="rounded-md border border-line bg-bg-1">
        {items.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-text-2">항목 없음</p>
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
                    <span className={`rounded px-1.5 py-0.5 text-[10px] ${statusTone[m.status] ?? "bg-bg-3 text-muted"}`}>
                      {m.status}
                    </span>
                    {m.status === "failed" && (
                      <Form method="post">
                        <input type="hidden" name="id" value={m.id} />
                        <button type="submit" disabled={busy} className="text-xs text-accent hover:underline">
                          재시도
                        </button>
                      </Form>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-3 text-[11px] text-text-2">
                  <span>시도 {m.attemptCount}회</span>
                  <span suppressHydrationWarning>{m.createdAt.replace("T", " ").replace("Z", "")}</span>
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
      </div>
    </div>
  );
}
