import { useEffect } from "react";
import { Form, useNavigation, useRevalidator, useSearchParams } from "react-router";
import type { Route } from "./+types/queue";
import { ApiError, apiFetch, type QueueItem } from "~/lib/api.server";
import { useT } from "~/lib/i18n";
import { requireAdmin } from "~/lib/session.server";
import { Badge, Button, Card, EmptyText, ErrorBanner, PageTitle, TimeText, type BadgeTone } from "~/components";

// Outbound queue — filter by status, retry failed entries.

export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await requireAdmin(request);
  const url = new URL(request.url);
  const status = url.searchParams.get("status") ?? "";

  const [itemList, statMap] = await Promise.all([
    apiFetch<QueueItem[]>(user.idToken, `/api/admin/queue?status=${status}`),
    apiFetch<Record<string, number>>(user.idToken, "/api/admin/queue/stat"),
  ]);
  return { itemList: itemList ?? [], statMap, status };
};

export const action = async ({ request }: Route.ActionArgs) => {
  const user = await requireAdmin(request);
  const form = await request.formData();
  const intent = form.get("intent");
  try {
    if (intent === "cancel") {
      await apiFetch(user.idToken, `/api/admin/queue/${form.get("id")}/cancel`, {
        method: "POST",
      });
    } else {
      await apiFetch(user.idToken, `/api/admin/queue/${form.get("id")}/retry`, {
        method: "POST",
      });
    }
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
  canceled: "muted",
};

const filterList = [
  { value: "", key: "queue.filterAll" },
  { value: "pending", key: "queue.filterPending" },
  { value: "sent", key: "queue.filterSent" },
  { value: "failed", key: "queue.filterFailed" },
  { value: "canceled", key: "queue.filterCanceled" },
] as const;

export default function Queue({ loaderData, actionData }: Route.ComponentProps) {
  const { itemList, statMap, status } = loaderData;
  const t = useT();
  const [, setSearchParams] = useSearchParams();
  const nav = useNavigation();
  const busy = nav.state !== "idle";
  const revalidator = useRevalidator();

  // Queue state is inherently live — poll every 10s while the tab is visible.
  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible" && revalidator.state === "idle") {
        revalidator.revalidate();
      }
    }, 10_000);
    return () => clearInterval(id);
  }, [revalidator]);

  return (
    <div className="flex flex-col gap-6">
      <PageTitle
        title={t("queue.title")}
        aside={
          <p className="text-xs text-text-2">
            {t("queue.stat", {
              pending: statMap.pending ?? 0,
              sent: statMap.sent ?? 0,
              failed: statMap.failed ?? 0,
            })}
          </p>
        }
      />

      <ErrorBanner message={actionData && !actionData.ok ? actionData.error : null} />

      <div className="flex gap-1">
        {filterList.map((f) => (
          <button
            key={f.value}
            type="button"
            aria-pressed={status === f.value}
            onClick={() => setSearchParams(f.value ? { status: f.value } : {})}
            className={`rounded-md px-3 py-1.5 text-xs transition-colors duration-100 ${
              status === f.value ? "bg-bg-3 text-text-0" : "text-text-2 hover:bg-bg-2"
            }`}
          >
            {t(f.key)}
          </button>
        ))}
      </div>

      <Card>
        {itemList.length === 0 ? (
          <EmptyText>{t("queue.empty")}</EmptyText>
        ) : (
          <ul className="divide-y divide-line">
            {itemList.map((m) => (
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
                        <input type="hidden" name="intent" value="retry" />
                        <input type="hidden" name="id" value={m.id} />
                        <Button variant="link" disabled={busy}>
                          {t("common.retry")}
                        </Button>
                      </Form>
                    )}
                    {m.status === "pending" && (
                      <Form method="post">
                        <input type="hidden" name="intent" value="cancel" />
                        <input type="hidden" name="id" value={m.id} />
                        <Button
                          variant="linkDanger"
                          disabled={busy}
                          confirmMessage={t("common.confirmCancelQueue")}
                        >
                          {t("common.cancel")}
                        </Button>
                      </Form>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-3 text-[11px] text-text-2">
                  <span>{t("queue.attemptCount", { count: m.attemptCount })}</span>
                  <TimeText value={m.createdAt} />
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
