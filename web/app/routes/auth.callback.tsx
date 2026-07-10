import { redirect } from "react-router";
import type { Route } from "./+types/auth.callback";
import { apiFetch, ApiError } from "~/lib/api.server";
import { translate } from "~/i18n";
import { getLocale } from "~/lib/locale.server";
import { decodeClaims, exchangeCode, publicOrigin } from "~/lib/oidc.server";
import { getSession, sessionStorage, type SessionUser } from "~/lib/session.server";

// IdP callback: verify state → exchange code → provision → store the user in the session.
export const loader = async ({ request }: Route.LoaderArgs) => {
  const url = new URL(request.url);
  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  const locale = await getLocale(request);

  const session = await getSession(request);
  const expectedState = session.get("oauthState") as string | undefined;
  if (!code || !state || state !== expectedState) {
    throw new Response(translate(locale, "auth.invalidResponse"), { status: 400 });
  }

  const redirectUri = `${publicOrigin(request)}/auth/callback`;
  const tokenSet = await exchangeCode(code, redirectUri);
  const claims = decodeClaims(tokenSet.idToken);

  // JIT provisioning — create or refresh the account (idempotent). Sign-in is
  // allowed even for emails on unregistered domains; the account just has no
  // address or mailbox yet. Admin status comes from the OIDC group, so the
  // admin console works even against an empty database.
  // (The Go API verifies the token and creates account/address/INBOX.)
  try {
    await apiFetch(tokenSet.idToken, "/api/me/provision", { method: "POST" });
  } catch (e) {
    if (e instanceof ApiError) {
      throw new Response(translate(locale, "auth.provisionFailed", { message: e.message }), {
        status: 403,
      });
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
