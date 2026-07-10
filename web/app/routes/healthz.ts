// Liveness/readiness probe endpoint — must not touch the session store.
// (Pointing the probe at /login would create a throwaway server-side session
// every 10s once sessions moved off the cookie.)
export const loader = () => new Response("ok", { status: 200 });
