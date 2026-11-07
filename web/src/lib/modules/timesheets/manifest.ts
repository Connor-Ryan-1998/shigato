import type { ModuleManifest } from "../registry";

export const timesheets: ModuleManifest = {
  name: "timesheets",
  label: "Timesheets",
  nav: [{ href: "billing-run", label: "Billing run", perm: "invoices_write" }],
  dashboardCard: () => import("./DashboardCard.svelte"),
};
