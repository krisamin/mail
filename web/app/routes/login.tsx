import { redirect } from "react-router";
import type { Route } from "./+types/login";
import { buildAuthorizeUrl, publicOrigin } from "~/lib/oidc.server";
import { getSession, sessionStorage } from "~/lib/session.server";

// /login → IdP authorize로 리다이렉트 (state는 세션에 저장)
export const loader = async ({ request }: Route.LoaderArgs) => {
  const url = new URL(request.url);
  const redirectUri = `${publicOrigin(request)}/auth/callback`;
  const state = crypto.randomUUID();

  const session = await getSession(request);
  session.set("oauthState", state);
  // 로그인 후 돌아갈 곳 (기본 /)
  session.set("returnTo", url.searchParams.get("returnTo") ?? "/");

  return redirect(await buildAuthorizeUrl(redirectUri, state), {
    headers: { "Set-Cookie": await sessionStorage.commitSession(session) },
  });
};
