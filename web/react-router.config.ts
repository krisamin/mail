import type { Config } from "@react-router/dev/config";

// Behind the ingress the server sees `http://<pod>:3000/...` as the request
// URL while the browser sends `Origin: https://mail.krisam.in`. React Router's
// CSRF guard compares the two and refuses every action with 400 ("Unexpected
// Server Error" on screen) unless the public host is listed here.
//
// Build-time value, so a different deployment passes MAIL_ALLOWED_ORIGINS
// (comma separated) to `docker build --build-arg`.
const allowedActionOrigins = (process.env.MAIL_ALLOWED_ORIGINS ?? "mail.krisam.in")
  .split(",")
  .map((host) => host.trim())
  .filter(Boolean);

export default {
  ssr: true,
  allowedActionOrigins,
} satisfies Config;
