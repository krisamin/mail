import { type RouteConfig, index, layout, prefix, route } from "@react-router/dev/routes";

export default [
  index("routes/home.tsx"),
  route("healthz", "routes/healthz.ts"),
  route("login", "routes/login.tsx"),
  route("auth/callback", "routes/auth.callback.tsx"),
  route("logout", "routes/logout.tsx"),
  route("account", "routes/account.tsx"),
  route("mail-file/*", "routes/mail/file.ts"),
  ...prefix("mail", [
    layout("routes/mail/layout.tsx", [
      index("routes/mail/index.tsx"),
      route("compose", "routes/mail/compose.tsx"),
      route(":mailbox", "routes/mail/list.tsx"),
      route(":mailbox/:id", "routes/mail/detail.tsx"),
    ]),
  ]),
  ...prefix("admin", [
    layout("routes/admin/layout.tsx", [
      index("routes/admin/index.tsx"),
      route("domain", "routes/admin/domain.tsx"),
      route("account", "routes/admin/account.tsx"),
      route("relay", "routes/admin/relay.tsx"),
      route("queue", "routes/admin/queue.tsx"),
      route("system", "routes/admin/system.tsx"),
    ]),
  ]),
] satisfies RouteConfig;
