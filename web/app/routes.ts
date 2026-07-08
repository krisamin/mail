import { type RouteConfig, index, layout, prefix, route } from "@react-router/dev/routes";

export default [
  index("routes/home.tsx"),
  route("login", "routes/login.tsx"),
  route("auth/callback", "routes/auth.callback.tsx"),
  route("logout", "routes/logout.tsx"),
  route("account", "routes/account.tsx"),
  ...prefix("admin", [
    layout("routes/admin/layout.tsx", [
      index("routes/admin/index.tsx"),
      route("domain", "routes/admin/domain.tsx"),
      route("domain/:domainId/account", "routes/admin/account.tsx"),
      route("queue", "routes/admin/queue.tsx"),
    ]),
  ]),
] satisfies RouteConfig;
