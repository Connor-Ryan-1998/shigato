import type { ModuleManifest } from "../registry";

export const property: ModuleManifest = {
  name: "property",
  label: "Property",
  nav: [{ href: "properties", label: "Properties", perm: "property_read", group: "property" }],
  dashboardCard: () => import("./DashboardCard.svelte"),
};
