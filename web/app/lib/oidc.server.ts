// OIDC Authorization Code Flow helpers (standards only, no library).
// discovery → authorize URL → code exchange → id_token claim parsing.

const ISSUER = process.env.MAIL_OIDC_ISSUER ?? "http://localhost:8480/realms/mail";
const CLIENT_ID = process.env.MAIL_OIDC_CLIENT_ID ?? "mail-web";
const CLIENT_SECRET = process.env.MAIL_OIDC_CLIENT_SECRET ?? "mail-web-dev-secret";
// Dev Keycloak attaches claims via client-level mappers, so "openid" is enough.
// Real IdPs (Authentik etc.) need "openid profile email" for email/groups claims.
const SCOPE = process.env.MAIL_OIDC_SCOPE ?? "openid";

/** Behind a reverse proxy the request origin looks like http — pin via env in production. */
export const publicOrigin = (request: Request): string =>
  process.env.MAIL_PUBLIC_ORIGIN ?? new URL(request.url).origin;

type Discovery = {
  authorization_endpoint: string;
  token_endpoint: string;
  end_session_endpoint?: string;
};

let discoveryCache: Discovery | null = null;

const discover = async (): Promise<Discovery> => {
  if (discoveryCache) return discoveryCache;
  // Some IdPs (Authentik) end the issuer with a slash — avoid double slashes.
  const base = ISSUER.replace(/\/$/, "");
  const res = await fetch(`${base}/.well-known/openid-configuration`);
  if (!res.ok) throw new Error(`OIDC discovery failed: ${res.status}`);
  discoveryCache = (await res.json()) as Discovery;
  return discoveryCache;
};

/** Sign-in start URL. state is a random CSRF token (stored in the session, checked in the callback). */
export const buildAuthorizeUrl = async (redirectUri: string, state: string): Promise<string> => {
  const d = await discover();
  const params = new URLSearchParams({
    response_type: "code",
    client_id: CLIENT_ID,
    redirect_uri: redirectUri,
    // Which scopes yield which claims (groups/email/username) depends on the
    // IdP — dev Keycloak: "openid" (client mapper), Authentik: "openid profile email".
    scope: SCOPE,
    state,
  });
  return `${d.authorization_endpoint}?${params}`;
};

export type TokenSet = { idToken: string; accessToken: string };

/** Exchange the callback code for tokens. */
export const exchangeCode = async (code: string, redirectUri: string): Promise<TokenSet> => {
  const d = await discover();
  const res = await fetch(d.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      client_id: CLIENT_ID,
      client_secret: CLIENT_SECRET,
      code,
      redirect_uri: redirectUri,
    }),
  });
  if (!res.ok) throw new Error(`token exchange failed: ${res.status} ${await res.text()}`);
  const body = (await res.json()) as { id_token: string; access_token: string };
  return { idToken: body.id_token, accessToken: body.access_token };
};

export type IdClaims = {
  sub: string;
  name?: string;
  preferred_username?: string;
  email?: string;
  groups?: string[];
};

/** Decode the id_token payload (the Go API re-verifies via JWKS — display only here). */
export const decodeClaims = (idToken: string): IdClaims => {
  const payload = idToken.split(".")[1];
  if (!payload) throw new Error("malformed token");
  const pad = payload + "=".repeat((4 - (payload.length % 4)) % 4);
  return JSON.parse(Buffer.from(pad, "base64url").toString()) as IdClaims;
};

/** IdP logout URL (null when unsupported — session-only logout). */
export const buildLogoutUrl = async (idToken: string, redirectUri: string): Promise<string | null> => {
  const d = await discover();
  if (!d.end_session_endpoint) return null;
  const params = new URLSearchParams({
    id_token_hint: idToken,
    post_logout_redirect_uri: redirectUri,
  });
  return `${d.end_session_endpoint}?${params}`;
};
