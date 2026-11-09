import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock("$app/state", () => ({
  page: {
    params: { org: "acme", id: "4" },
    url: new URL("http://localhost/acme"),
  },
}));
vi.mock("../../../lib/session.svelte", () => ({
  session: { currentOrg: { slug: "acme", perms: ["property_read", "property_write"] } },
}));
vi.mock("../../../lib/api", () => ({
  api: apiMock,
  ApiError: class ApiError extends Error {},
}));

import Page from "./+page.svelte";

function route(method: string, path: string) {
  if (path.endsWith("/properties"))
    return Promise.resolve([{ id: 1, name: "12 High St", residential: true }]);
  if (path.endsWith("/contacts"))
    return Promise.resolve([{ id: 7, name: "Tina Tenant", kind: "tenant" }]);
  if (path.endsWith("/leases?property_id=1"))
    return Promise.resolve([
      {
        id: 4,
        property_id: 1,
        start_date: "2026-01-01",
        frequency: "weekly",
        rent_changes: [{ effective_date: "2026-01-01", amount: 50000 }],
        tenants: [{ contact_id: 7, primary: true }],
      },
    ]);
  return Promise.resolve({});
}

describe("properties page", () => {
  beforeEach(() => {
    apiMock.mockReset();
    apiMock.mockImplementation(route);
  });

  it("lists properties with their leases and links to lease creation", async () => {
    render(Page);
    expect(await screen.findByText("Lease from 2026-01-01")).toBeTruthy();
    expect(screen.getByText(/Tina Tenant · \$500\.00 weekly/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'New lease' }).getAttribute('href')).toBe('/acme/leases/new');
    expect(screen.queryByLabelText('Rent ($)')).toBeNull();
  });
});
