import { type RouteConfig, index, layout, prefix, route } from "@react-router/dev/routes";

// Route tree mirrors the shell: three areas (mail, setting, admin), each with
// its own secondary column, all under one rail.
export default [
  index("routes/home.tsx"),
  route("healthz", "routes/healthz.ts"),
  route("login", "routes/login.tsx"),
  route("auth/callback", "routes/auth.callback.tsx"),
  route("logout", "routes/logout.tsx"),
  route("preference", "routes/preference.ts"),
  route("mail-file/*", "routes/mail/file.ts"),
  route("mail/folder", "routes/mail/folder.ts"),

  ...prefix("mail", [
    layout("routes/mail/layout.tsx", [
      index("routes/mail/index.tsx"),
      route(":mailbox", "routes/mail/list.tsx", [
        route("compose", "routes/mail/compose.tsx"),
        route(":id", "routes/mail/detail.tsx"),
      ]),
    ]),
  ]),

  ...prefix("setting", [
    layout("routes/setting/layout.tsx", [
      index("routes/setting/index.tsx"),
      route("address", "routes/setting/address.tsx"),
      route("app-password", "routes/setting/app-password.tsx"),
      route("filter", "routes/setting/filter.tsx"),
      route("appearance", "routes/setting/appearance.tsx"),
    ]),
  ]),

  ...prefix("admin", [
    layout("routes/admin/layout.tsx", [
      index("routes/admin/index.tsx"),
      route("domain", "routes/admin/domain.tsx"),
      route("account", "routes/admin/account.tsx"),
      route("group", "routes/admin/group.tsx"),
      route("relay", "routes/admin/relay.tsx"),
      route("queue", "routes/admin/queue.tsx"),
      route("system", "routes/admin/system.tsx"),
    ]),
  ]),
] satisfies RouteConfig;
