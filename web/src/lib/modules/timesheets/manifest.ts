import type { ModuleManifest } from "../registry";

export const timesheets: ModuleManifest = {
  name: "timesheets",
  label: "Projects",
  nav: [
    { href: "projects", label: "Projects", perm: "timesheets_read", group: "projects" },
    { href: "time", label: "Time-Logging", perm: "timesheets_read", group: "projects" },
  ],
  dashboardCard: () => import("./DashboardCard.svelte"),
};
