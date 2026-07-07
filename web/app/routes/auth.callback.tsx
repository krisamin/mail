import { redirect } from "react-router";
import type { Route } from "./+types/auth.callback";
import { apiFetch, ApiError } from "~/lib/api.server";
import { decodeClaims, exchangeCode } from "~/lib/oidc.server";
import { getSession, sessionStorage, type SessionUser } from "~/lib/session.server";

// IdP 콜백: state 대조 → code 교환 → 도메인 게이트 → 세션에 유저 저장
export const loader = async ({ request }: Route.LoaderArgs) => {
  const url = new URL(request.url);
  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");

  const session = await getSession(request);
  const expectedState = session.get("oauthState") as string | undefined;
  if (!code || !state || state !== expectedState) {
    throw new Response("잘못된 인증 응답", { status: 400 });
  }

  const redirectUri = `${url.origin}/auth/callback`;
  const tokens = await exchangeCode(code, redirectUri);
  const claims = decodeClaims(tokens.idToken);

  // ── 로그인 게이트: email 도메인이 이 서버에 등록된 도메인이어야 한다.
  // (Go API가 토큰 검증 + 도메인 조회 — 여기서 거부하면 세션 자체를 안 만든다)
  try {
    const gate = await apiFetch<{ domainExists: boolean }>(tokens.idToken, "/api/me/gate");
    if (!gate.domainExists) {
      const domain = (claims.email ?? "").split("@")[1] ?? "?";
      throw new Response(
        `이 메일 서버에 등록되지 않은 도메인이에요: @${domain}`,
        { status: 403 },
      );
    }
  } catch (e) {
    if (e instanceof Response) throw e;
    if (e instanceof ApiError) {
      throw new Response(`로그인 확인 실패: ${e.message}`, { status: 403 });
    }
    throw e;
  }

  const user: SessionUser = {
    sub: claims.sub,
    name: claims.name ?? claims.preferred_username ?? claims.sub,
    email: claims.email ?? "",
    groups: claims.groups ?? [],
    idToken: tokens.idToken,
  };

  const returnTo = (session.get("returnTo") as string | undefined) ?? "/";
  session.unset("oauthState");
  session.unset("returnTo");
  session.set("user", user);

  return redirect(returnTo, {
    headers: { "Set-Cookie": await sessionStorage.commitSession(session) },
  });
};
