import { createCookieSessionStorage, redirect } from "react-router";
import { decodeClaims, refreshTokens } from "./oidc.server";

// Session.
//
// The cookie carries only what is small and long-lived: who you are and a
// refresh token. The id_token (~4KB, and often minutes old) never goes in it —
// it would blow the 4096-byte cookie limit and expire long before the session
// does. It lives in a process-local cache instead, and when it is missing or
// stale the refresh token silently buys a new one.
//
// That combination is what makes the session survive the two things that used
// to end it: the IdP's short token lifetime (five minutes here) and a web pod
// restart (the previous in-memory session store forgot everyone on deploy).
//
// Dev default secret; production must set SESSION_SECRET.
const secret = process.env.SESSION_SECRET ?? "mail-dev-session-secret";

export const sessionStorage = createCookieSessionStorage({
  cookie: {
    // Renamed whenever the stored shape changes: an old cookie still decrypts
    // under the same secret and would be read as the new shape, which fails in
    // confusing ways. A new name makes stale cookies invisible.
    name: "__mail_session2",
    httpOnly: true,
    path: "/",
    sameSite: "lax",
    secrets: [secret],
    secure: process.env.NODE_ENV === "production",
    maxAge: 60 * 60 * 24 * 30, // 30d — the refresh token's own lifetime
  },
});

export type SessionUser = {
  sub: string;
  name: string;
  email: string;
  groupList: string[];
  idToken: string;
};

/** What actually lives in the cookie. */
type StoredUser = Omit<SessionUser, "idToken"> & { refreshToken: string };

export const getSession = (request: Request) =>
  sessionStorage.getSession(request.headers.get("Cookie"));

/** id_token cache, keyed by refresh token. Process-local on purpose: losing it
 *  costs one refresh round-trip, never a sign-out. */
const tokenCache = new Map<string, { idToken: string; expiresAt: number }>();

const claimExpiry = (idToken: string): number => {
  try {
    const payload = idToken.split(".")[1];
    if (!payload) return 0;
    const pad = payload + "=".repeat((4 - (payload.length % 4)) % 4);
    const claims = JSON.parse(Buffer.from(pad, "base64url").toString()) as { exp?: number };
    return claims.exp ? claims.exp * 1000 : 0;
  } catch {
    return 0;
  }
};

/** Usable for a little longer — 60s of clock-skew margin so a token never
 *  expires mid-request at the Go API's JWKS check. */
const alive = (expiresAt: number) => expiresAt - 60_000 > Date.now();

export const rememberToken = (refreshToken: string, idToken: string) => {
  tokenCache.set(refreshToken, { idToken, expiresAt: claimExpiry(idToken) });
};

export const forgetToken = (refreshToken: string) => {
  tokenCache.delete(refreshToken);
};

/** In-flight refreshes, so a page firing five parallel loaders performs one
 *  token exchange rather than five. */
const inFlightMap = new Map<string, Promise<string | null>>();

const freshIdToken = async (stored: StoredUser): Promise<string | null> => {
  const cached = tokenCache.get(stored.refreshToken);
  if (cached && alive(cached.expiresAt)) return cached.idToken;

  const running = inFlightMap.get(stored.refreshToken);
  if (running) return running;

  const task = (async () => {
    const next = await refreshTokens(stored.refreshToken);
    if (!next) {
      tokenCache.delete(stored.refreshToken);
      return null;
    }
    rememberToken(stored.refreshToken, next.idToken);
    // A rotating IdP hands back a new refresh token. The cookie still carries
    // the old one for this request, so index the new id_token under both.
    if (next.refreshToken !== stored.refreshToken) {
      rememberToken(next.refreshToken, next.idToken);
    }
    return next.idToken;
  })().finally(() => inFlightMap.delete(stored.refreshToken));

  inFlightMap.set(stored.refreshToken, task);
  return task;
};

export const getUser = async (request: Request): Promise<SessionUser | null> => {
  const session = await getSession(request);
  const stored = session.get("user") as StoredUser | undefined;
  if (!stored) return null;

  const idToken = await freshIdToken(stored);
  if (!idToken) return null;

  // Claims can change between sign-ins (name, group membership); trust the
  // token we just got over what the cookie remembered.
  let name = stored.name;
  let email = stored.email;
  let groupList = stored.groupList;
  try {
    const claims = decodeClaims(idToken);
    name = claims.name ?? claims.preferred_username ?? name;
    email = claims.email ?? email;
    groupList = claims.groups ?? groupList;
  } catch {
    // keep what the cookie had
  }

  return { sub: stored.sub, name, email, groupList, idToken };
};

/** Writes the session cookie. Called from the OIDC callback only. */
export const storeUser = async (
  request: Request,
  user: Omit<SessionUser, "idToken">,
  tokenSet: { idToken: string; refreshToken: string },
) => {
  const session = await getSession(request);
  session.unset("oauthState");
  session.unset("returnTo");
  const stored: StoredUser = {
    sub: user.sub,
    name: user.name,
    email: user.email,
    groupList: user.groupList,
    refreshToken: tokenSet.refreshToken,
  };
  session.set("user", stored);
  rememberToken(tokenSet.refreshToken, tokenSet.idToken);
  if (!tokenSet.refreshToken) {
    // Then the session can only live as long as this id_token and a restart
    // signs everyone out. authentik, for one, only issues refresh tokens when
    // offline_access is in MAIL_OIDC_SCOPE — say so out loud rather than
    // letting it show up as "I keep getting logged out".
    console.warn(
      "session: the IdP returned no refresh token — sign-ins will expire with the id_token. " +
        "Add offline_access to MAIL_OIDC_SCOPE if the IdP requires it.",
    );
  }
  return sessionStorage.commitSession(session);
};

/** For login-required routes — throws a /login redirect when there is no
 *  user or the session can no longer produce a token. RR runs parent/child
 *  loaders in parallel, so child loaders must call this too (leaning on the
 *  parent guard alone can null-deref). */
export const requireUser = async (request: Request): Promise<SessionUser> => {
  const user = await getUser(request);
  if (!user) {
    const url = new URL(request.url);
    throw redirect(`/login?returnTo=${encodeURIComponent(url.pathname + url.search)}`);
  }
  return user;
};

export const ADMIN_GROUP = process.env.MAIL_ADMIN_GROUP ?? "mail-admin";

export const isAdmin = (user: SessionUser): boolean => user.groupList.includes(ADMIN_GROUP);

export const requireAdmin = async (request: Request): Promise<SessionUser> => {
  const user = await requireUser(request);
  if (!isAdmin(user)) {
    throw new Response("admin only", { status: 403 });
  }
  return user;
};
