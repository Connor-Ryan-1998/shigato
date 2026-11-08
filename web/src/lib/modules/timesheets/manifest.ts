import type { ModuleManifest } from "../registry";

export const timesheets: ModuleManifest = {
  name: "timesheets",
  label: "Projects",
  nav: [
    { href: "projects", label: "Projects", perm: "timesheets_read" },
    { href: "time", label: "Time-Logging", perm: "timesheets_read" },
    { href: "billing-run", label: "Billing run", perm: "invoices_write" },
  ],
  dashboardCard: () => import("./DashboardCard.svelte"),
};
