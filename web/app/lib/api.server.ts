// Go Admin API helpers — called server-side only, from RR7 loaders/actions.
// The session's id_token is forwarded as Bearer; the Go side re-verifies via
// JWKS and re-checks groups (defense in depth).

const API_BASE = process.env.MAIL_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export const apiFetch = async <T>(
  idToken: string,
  path: string,
  init?: { method?: string; body?: unknown; timeoutMs?: number },
): Promise<T> => {
  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method: init?.method ?? "GET",
      headers: {
        Authorization: `Bearer ${idToken}`,
        ...(init?.body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
      // Timeout — a hung Go API must not freeze every loader/action with it.
      // (System "external" probes pass a longer budget explicitly.)
      signal: AbortSignal.timeout(init?.timeoutMs ?? 10_000),
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "TimeoutError") {
      throw new ApiError(504, "API timeout");
    }
    throw e;
  }
  if (res.status === 204) return undefined as T;
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new ApiError(res.status, (body as { error?: string }).error ?? `API ${res.status}`);
  }
  return body as T;
};

// ── DTO types (kept in sync with Go internal/api) ───────────

export type Domain = {
  id: number;
  name: string;
  active: boolean;
  createdAt: string;
  dkimSelector: string;
  dkimPublicTxt?: string;
  relayId?: number | null;
};

export type Account = {
  id: number;
  subject: string;
  email: string;
  kind: "user" | "service";
  active: boolean;
  createdAt: string;
};

export type AppPassword = {
  id: number;
  label: string;
  scopeList: string[];
  lastUsed: string | null;
  createdAt: string;
  revoked: boolean;
};

export type Address = {
  id: number;
  domainId: number;
  domainName: string;
  localPart: string; // '*' = catch-all
  accountId: number;
  accountEmail: string;
  createdAt: string;
};

export type QueueItem = {
  id: number;
  from: string;
  rcpt: string;
  status: string;
  attemptCount: number;
  nextAttemptAt: string;
  lastError: string;
  createdAt: string;
};

export type DKIMResult = { selector: string; dnsName: string; dnsTxt: string };

export type Relay = {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  starttls: boolean;
  isDefault: boolean;
  active: boolean;
  createdAt: string;
  hasPassword: boolean;
};

export type DnsCheck = {
  status: "ok" | "warn" | "missing";
  found: string;
  expected?: string;
  note?: string;
};

export type DnsVerify = {
  domain: string;
  mx: DnsCheck;
  spf: DnsCheck;
  dkim: DnsCheck;
  dmarc: DnsCheck;
  // Client autodiscovery records (SRV names follow the DNS service labels).
  srvImaps: DnsCheck;
  srvSubmissions: DnsCheck;
  srvSubmission: DnsCheck;
  autoconfig: DnsCheck;
};
