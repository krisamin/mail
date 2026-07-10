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

  return redirect(await buildAuthorizeUrl(redirectUri, state), {
    headers: { "Set-Cookie": await sessionStorage.commitSession(session) },
  });
};
