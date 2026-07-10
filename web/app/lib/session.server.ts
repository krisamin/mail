import { createMemorySessionStorage, redirect } from "react-router";

// Session storage — server-side (in-memory), the cookie carries only a
// session ID. The id_token alone is ~4KB which overflows the 4096-byte
// browser cookie limit once state/returnTo are added on the /login hop
// ("Cookie length will exceed browser maximum"), so token material must
// never live in the cookie itself.
// Trade-off: sessions reset on pod restart — fine here (single replica,
// and the IdP SSO session silently re-issues on the /login round-trip).
// Dev default secret; production must set SESSION_SECRET.
const secret = process.env.SESSION_SECRET ?? "mail-dev-session-secret";

export const sessionStorage = createMemorySessionStorage({
  cookie: {
    // Renamed from "__mail_session" when sessions moved server-side: the old
    // cookie held the full session data under the same secret, so it still
    // parses — but as an object where a session ID string is expected, which
    // silently breaks the store lookup (state mismatch on OIDC callback).
    // A new name makes stale cookies invisible; /login clears the legacy one.
    name: "__mail_sid",
    httpOnly: true,
    path: "/",
    sameSite: "lax",
    secrets: [secret],
    secure: process.env.NODE_ENV === "production",
    maxAge: 60 * 60 * 8, // 8h
  },
});

export type SessionUser = {
  sub: string;
  name: string;
  email: string;
  groupList: string[];
  idToken: string;
};

export const getSession = (request: Request) =>
  sessionStorage.getSession(request.headers.get("Cookie"));

// Token liveness check — the session lives 8h but the id_token expires much
// sooner (per IdP settings). An expired token would 401 at the Go API's JWKS
// check and kill the loader, so treat it as signed-out here instead. The
// guard bounces to /login, and as long as the IdP SSO session is alive the
// round-trip re-issues a token silently.
const tokenAlive = (idToken: string): boolean => {
  try {
    const payload = idToken.split(".")[1];
    if (!payload) return false;
    const pad = payload + "=".repeat((4 - (payload.length % 4)) % 4);
    const claims = JSON.parse(Buffer.from(pad, "base64url").toString()) as { exp?: number };
    if (!claims.exp) return true;
    return claims.exp - 60 > Date.now() / 1000; // 60s clock-skew margin
  } catch {
    return false;
  }
};

export const getUser = async (request: Request): Promise<SessionUser | null> => {
  const session = await getSession(request);
  const user = session.get("user") as SessionUser | undefined;
  if (!user) return null;
  if (!tokenAlive(user.idToken)) return null;
  return user;
};

/** For login-required routes — throws a /login redirect when there is no
 *  user or the token has expired. RR7 runs parent/child loaders in parallel,
 *  so child loaders must call this too (leaning on the parent guard alone
 *  can null-deref). */
export const requireUser = async (request: Request): Promise<SessionUser> => {
  const user = await getUser(request);
  if (!user) {
    const url = new URL(request.url);
    throw redirect(`/login?returnTo=${encodeURIComponent(url.pathname + url.search)}`);
  }
  return user;
};

export const ADMIN_GROUP = process.env.MAIL_ADMIN_GROUP ?? "mail-admin";

export const isAdmin = (user: SessionUser | null): boolean =>
  !!user && user.groupList.some((g) => g === ADMIN_GROUP || g === `/${ADMIN_GROUP}`);
