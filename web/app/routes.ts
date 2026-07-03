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
      route("domains", "routes/admin/domains.tsx"),
      route("domains/:domainId/users", "routes/admin/users.tsx"),
      route("queue", "routes/admin/queue.tsx"),
    ]),
  ]),
] satisfies RouteConfig;
