import { redirect } from "react-router";
import type { Route } from "./+types/auth.callback";
import { apiFetch, ApiError } from "~/lib/api.server";
import { decodeClaims, exchangeCode, publicOrigin } from "~/lib/oidc.server";
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

  const redirectUri = `${publicOrigin(request)}/auth/callback`;
  const tokenSet = await exchangeCode(code, redirectUri);
  const claims = decodeClaims(tokenSet.idToken);

  // ── JIT 프로비저닝: 계정을 만들거나 갱신한다 (멱등). 도메인 미등록
  // email이어도 로그인은 허용 — 계정만 생기고 주소/메일함이 없을 뿐.
  // admin은 OIDC 그룹으로 판정되므로 빈 DB에서도 관리 화면에 들어갈 수 있다.
  // (Go API가 토큰 검증 + 계정/주소/INBOX 생성)
  try {
    await apiFetch(tokenSet.idToken, "/api/me/provision", { method: "POST" });
  } catch (e) {
    if (e instanceof ApiError) {
      throw new Response(`로그인 확인 실패: ${e.message}`, { status: 403 });
    }
    throw e;
  }

  const user: SessionUser = {
    sub: claims.sub,
    name: claims.name ?? claims.preferred_username ?? claims.sub,
    email: claims.email ?? "",
    groupList: claims.groups ?? [],
    idToken: tokenSet.idToken,
  };

  const returnTo = (session.get("returnTo") as string | undefined) ?? "/";
  session.unset("oauthState");
  session.unset("returnTo");
  session.set("user", user);

  return redirect(returnTo, {
    headers: { "Set-Cookie": await sessionStorage.commitSession(session) },
  });
};
