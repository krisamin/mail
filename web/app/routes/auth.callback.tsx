import { redirect } from "react-router";
import type { Route } from "./+types/auth.callback";
import { decodeClaims, exchangeCode } from "~/lib/oidc.server";
import { getSession, sessionStorage, type SessionUser } from "~/lib/session.server";

// IdP 콜백: state 대조 → code 교환 → 세션에 유저 저장
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
