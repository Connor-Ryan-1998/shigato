import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock("$app/state", () => ({
  page: {
    params: { org: "acme", id: "4" },
    url: new URL("http://localhost/acme"),
  },
}));
vi.mock("../../../../lib/session.svelte", () => ({
  session: { currentOrg: { slug: "acme", perms: ["property_read", "property_write"] } },
}));
vi.mock("../../../../lib/api", () => ({
  api: apiMock,
  ApiError: class ApiError extends Error {},
}));

import Page from "./+page.svelte";

const LEASE = {
  id: 4,
  start_date: "2026-01-01",
  frequency: "weekly",
  bond_amount: 200000,
  bond_status: "due",
  rent_changes: [{ id: 1, effective_date: "2026-01-01", amount: 50000 }],
  charges: [],
};
const LEDGER = {
  periods: [
    {
      start: "2026-01-01",
      end: "2026-01-07",
      amount_due: 50000,
      invoice_id: 9,
      invoice_status: "paid",
      paid: 50000,
    },
  ],
  opening_arrears: 0,
  arrears: 0,
  invoiced_not_due: 0,
  unissued: 0,
};

describe("lease page", () => {
  beforeEach(() => {
    apiMock.mockReset();
    apiMock.mockImplementation((m: string, p: string) =>
      m === "GET"
        ? Promise.resolve(p.endsWith("/ledger") ? LEDGER : LEASE)
        : Promise.resolve({ warnings: ["heads up"] }),
    );
  });

  it("shows the ledger and posts a rent change with its warnings", async () => {
    render(Page);
    expect(await screen.findByText("2026-01-01 to 2026-01-07")).toBeTruthy();
    expect(screen.getByText("paid").getAttribute("href")).toBe(
      "/acme/invoices/9",
    );
    await fireEvent.input(screen.getByLabelText("Rent ($)"), {
      target: { value: "520" },
    });
    await fireEvent.click(screen.getByText("Change rent"));
    expect(await screen.findByText("Rent change saved. heads up")).toBeTruthy();
    expect(apiMock).toHaveBeenCalledWith(
      "POST",
      "/api/v1/orgs/acme/leases/4/rent-changes",
      {
        effective_date: expect.any(String),
        amount: 52000,
      },
    );
  });
});
