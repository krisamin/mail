import { redirect } from "react-router";
import type { Route } from "./+types/logout";
import { buildLogoutUrl, publicOrigin } from "~/lib/oidc.server";
import { getSession, getUser, sessionStorage } from "~/lib/session.server";

// Destroy the session, then IdP logout when the endpoint exists.
export const loader = async ({ request }: Route.LoaderArgs) => {
  const user = await getUser(request);
  const session = await getSession(request);

  let target = "/";
  if (user) {
    const idpLogout = await buildLogoutUrl(user.idToken, publicOrigin(request));
    if (idpLogout) target = idpLogout;
  }
  return redirect(target, {
    headers: { "Set-Cookie": await sessionStorage.destroySession(session) },
  });
};
