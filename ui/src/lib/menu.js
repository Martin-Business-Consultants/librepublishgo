// The admin menu, the Rails admin's groups (NavigationHelper::ADMIN_MENU)
// for what this core has. A capability hides what a role can't reach.
export function adminMenu(collections, plugins = []) {
  const groups = [
    ["Content", [
      { id: "pages", label: "Pages", icon: "document", href: "/pages", cap: "pages:read", submenu: [
        { label: "All pages", href: "/pages" }, { label: "Add new", href: "/pages/new", cap: "pages:write" } ] },
      { id: "collections", label: "Collections", icon: "stack", href: "/collections", cap: "collections:read", submenu: [
        { label: "All collections", href: "/collections" }, { label: "Add new", href: "/collections/new", cap: "collections:write" },
        ...collections.slice(0, 12).map((c) => ({ label: c.name, href: `/collections/${c.slug}`, icon: "newspaper", cap: "entries:read" })) ] },
      { id: "globals", label: "Globals", icon: "world", href: "/globals", cap: "globals:read", submenu: [
        { label: "All globals", href: "/globals" }, { label: "Add new", href: "/globals/new", cap: "globals:write" } ] },
      { id: "media", label: "Media", icon: "image", href: "/media", cap: "assets:read" },
    ]],
    ["Insights", [
      { id: "revisions", label: "Review", icon: "check-circle", href: "/revisions", badge: "reviews" },
      { id: "audit", label: "Audit log", icon: "history", href: "/audit", cap: "audit_log:read" },
      { id: "trash", label: "Trash", icon: "trash", href: "/trash", cap: "trash:read" },
    ]],
    ["Tools", [
      { id: "tools", label: "Tools", icon: "wrench", href: "/blocks", submenu: [
        { label: "Block types", href: "/blocks", cap: "block_types:read" },
        { label: "Redirects", href: "/redirects", cap: "redirects:read" },
        { label: "Webhooks", href: "/webhooks", cap: "webhooks:read" },
        { label: "Backup", href: "/backup", cap: "tools:use" },
        { label: "Import", href: "/import", cap: "tools:use" } ] },
      { id: "plugins", label: "Plugins", icon: "package", href: "/plugins", cap: "plugins:read", exact: true },
    ]],
    ["Access", [
      { id: "users", label: "Users", icon: "everyone", href: "/users", cap: "users:read", submenu: [
        { label: "All users", href: "/users" }, { label: "Add new", href: "/users?new=1", cap: "users:write" } ] },
      { id: "roles", label: "Roles", icon: "lock", href: "/roles", cap: "roles:read" },
    ]],
    ["Settings", [
      { id: "settings", label: "Settings", icon: "settings-outline", href: "/settings", cap: "settings:read", submenu: [
        { label: "General", href: "/settings" }, { label: "Deploy", href: "/settings/deploy" }, { label: "Email", href: "/settings/email" }, { label: "Brand", href: "/settings/brand" }, { label: "AI", href: "/settings/ai" },
        { label: "Site repository", href: "/settings/github" }, { label: "Service tokens", href: "/settings/tokens" }, { label: "Updates", href: "/settings/updates", cap: "tools:use" } ] },
    ]],
  ];
  // Each plugin's menu joins the group it names (Tools when it names none).
  for (const m of plugins) {
    const item = { id: "plugin-" + m.key, label: m.label, icon: m.icon || "package", href: `/plugins/${m.key}/${m.items[0]?.screen}`,
      prefix: `/plugins/${m.key}/`, badge: "plugin:" + m.key, submenu: m.items.map((it) => ({ label: it.label, href: `/plugins/${m.key}/${it.screen}` })) };
    const g = groups.find(([name]) => name === (m.group || "Tools")) || groups.find(([name]) => name === "Tools");
    g[1].push(item);
  }
  return groups;
}
