import type { Route } from "./+types/file";
import { requireUser } from "~/lib/session.server";

// Streaming file proxy — attachments and raw .eml come from the Go API which
// requires a Bearer token, but browser <a download> links cannot carry
// headers. This resource route re-issues the request server-side with the
// session's token and streams the response through.

const API_BASE = process.env.MAIL_API_URL ?? "http://localhost:8080";

export const loader = async ({ request, params }: Route.LoaderArgs) => {
  const user = await requireUser(request);
  // splat is validated by allowlist — only the two file endpoints proxy
  const rest = params["*"] ?? "";
  if (!/^\d+\/(raw|attachment\/\d+)$/.test(rest)) {
    throw new Response("not found", { status: 404 });
  }
  const upstream = await fetch(`${API_BASE}/api/me/message/${rest}`, {
    headers: { Authorization: `Bearer ${user.idToken}` },
    signal: AbortSignal.timeout(60_000),
  });
  if (!upstream.ok) {
    throw new Response("not found", { status: upstream.status });
  }
  const headers = new Headers();
  for (const name of ["Content-Type", "Content-Disposition", "X-Content-Type-Options"]) {
    const v = upstream.headers.get(name);
    if (v) headers.set(name, v);
  }
  return new Response(upstream.body, { status: 200, headers });
};
