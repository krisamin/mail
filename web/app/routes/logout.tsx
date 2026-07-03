import { redirect } from "react-router";
import type { Route } from "./+types/logout";
import { buildLogoutUrl } from "~/lib/oidc.server";
import { getSession, getUser, sessionStorage } from "~/lib/session.server";

// 세션 파기 + IdP 로그아웃 (있으면)
export const loader = async ({ request }: Route.LoaderArgs) => {
  const url = new URL(request.url);
  const user = await getUser(request);
  const session = await getSession(request);

  let target = "/";
  if (user) {
    const idpLogout = await buildLogoutUrl(user.idToken, url.origin);
    if (idpLogout) target = idpLogout;
  }
  return redirect(target, {
    headers: { "Set-Cookie": await sessionStorage.destroySession(session) },
  });
};
