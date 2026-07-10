import { redirect } from "react-router";
import type { Route } from "./+types/login";
import { buildAuthorizeUrl, publicOrigin } from "~/lib/oidc.server";
import { getSession, sessionStorage } from "~/lib/session.server";

// /login → redirect to the IdP authorize endpoint (state stored in session).
export const loader = async ({ request }: Route.LoaderArgs) => {
  const url = new URL(request.url);
  const redirectUri = `${publicOrigin(request)}/auth/callback`;
  const state = crypto.randomUUID();

  const session = await getSession(request);
  session.set("oauthState", state);
  // Where to land after sign-in (default /).
  session.set("returnTo", url.searchParams.get("returnTo") ?? "/");

  const headers = new Headers();
  headers.append("Set-Cookie", await sessionStorage.commitSession(session));
  // Drop the legacy cookie-storage session (pre-server-side-session era) so
  // stale full-data cookies stop shadowing the new session ID cookie.
  headers.append("Set-Cookie", "__mail_session=; Path=/; HttpOnly; Max-Age=0");

  return redirect(await buildAuthorizeUrl(redirectUri, state), { headers });
};
