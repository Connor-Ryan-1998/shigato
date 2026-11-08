import type { ModuleManifest } from "../registry";

export const timesheets: ModuleManifest = {
  name: "timesheets",
  label: "Timesheets",
  nav: [
    { href: "time", label: "Time & projects", perm: "timesheets_read" },
    { href: "billing-run", label: "Billing run", perm: "invoices_write" },
  ],
  dashboardCard: () => import("./DashboardCard.svelte"),
};
