import { describe, expect, it, vi, beforeEach } from "vitest";
import { apiMock } from "../../../lib/test/mock-api";
import { setPage } from "../../../lib/test/app.svelte";
import { session } from "../../../lib/session.svelte";
import { signIn } from "../../../lib/test/session";
import { render, screen } from "@testing-library/svelte";

import Page from "./+page.svelte";
import { ApiError } from "../../../lib/api";

const PNL = [
  {
    key: "Acme",
    income: { Rent: 120000 },
    expenses: { Repairs: 20000 },
    total_income: 120000,
    total_expenses: 20000,
    net: 100000,
  },
];
const BAS = {
  period: "FY2027-Q1",
  figures_cents: { g1: 550000, "1a": 50000, "1b": 1000 },
};

const DETAIL = {
  field: "1A",
  income: [
    {
      payment_id: 9,
      invoice_id: 3,
      invoice_number: 42,
      paid_on: "2026-10-02",
      category: "Rent",
      gst_code: "GST",
      incl_gst: 55000,
      gst: 5000,
      ex_gst: 50000,
    },
  ],
};

function route(path: string) {
  if (path.includes("/reports/pnl?")) return Promise.resolve(PNL);
  if (path.includes("/reports/bas?")) return Promise.resolve(BAS);
  if (path.includes("/reports/bas/detail?")) return Promise.resolve(DETAIL);
  return Promise.reject(new Error("unexpected " + path));
}

describe("reports page", () => {
  beforeEach(() => {
    setPage({ params: { org: "acme" }, url: "http://localhost/acme/reports" });

    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 10, 7)); // 7 Nov 2026 -> FY2027 Q2
  });

  it("renders P&L group totals and BAS figures", async () => {
    apiMock.mockImplementation((_m: string, p: string) => route(p));
    render(Page);
    expect(await screen.findByText("Net")).toBeTruthy();
    expect(screen.getByText("$1,000.00")).toBeTruthy();
    expect(await screen.findByText("1A GST on sales")).toBeTruthy();
    expect(screen.getByText("$5,500.00")).toBeTruthy();
  });

  it("defaults to the current FY range and quarter", async () => {
    apiMock.mockImplementation((_m: string, p: string) => route(p));
    render(Page);
    await screen.findByText("Net");
    const paths = apiMock.mock.calls.map((c) => c[1] as string);
    expect(paths).toContain(
      "/api/v1/orgs/acme/reports/pnl?from=2026-07-01&to=2026-11-07&group_by=org",
    );
    expect(paths).toContain("/api/v1/orgs/acme/reports/bas?period=FY2027-Q2");
  });

  it("CSV links carry the same params", async () => {
    apiMock.mockImplementation((_m: string, p: string) => route(p));
    render(Page);
    await screen.findByText("Net");
    expect(screen.getByText("P&L CSV").getAttribute("href")).toBe(
      "/api/v1/orgs/acme/reports/pnl.csv?from=2026-07-01&to=2026-11-07&group_by=org",
    );
    expect(screen.getByText("BAS CSV").getAttribute("href")).toBe(
      "/api/v1/orgs/acme/reports/bas.csv?period=FY2027-Q2",
    );
  });

  it("shows a 422 inline", async () => {
    apiMock.mockImplementation((_m: string, p: string) =>
      p.includes("/bas?")
        ? Promise.reject(new ApiError(422, "org is not GST-registered"))
        : route(p),
    );
    render(Page);
    expect(await screen.findByText("org is not GST-registered")).toBeTruthy();
  });

  it("drills into a BAS field", async () => {
    apiMock.mockImplementation((_m: string, p: string) => route(p));
    render(Page);
    (await screen.findByRole("button", { name: "1A GST on sales" })).click();
    expect(await screen.findByText("1A detail")).toBeTruthy();
    expect(screen.getByText("Invoice 42")).toBeTruthy();
    const paths = apiMock.mock.calls.map((c) => c[1] as string);
    expect(paths).toContain(
      "/api/v1/orgs/acme/reports/bas/detail?period=FY2027-Q2&field=1A",
    );
  });

  it("hides rental reports without the property module", async () => {
    session.setModules([]);
    apiMock.mockImplementation((_m: string, p: string) => route(p));
    render(Page);
    await screen.findByText("Net");
    expect(screen.queryByRole("heading", { name: "Rental property tax" })).toBeNull();
  });

  it("links rental reports for the chosen year and property", async () => {
    signIn();
    session.setModules(["property", "reconciliation"]);
    apiMock.mockImplementation((_m: string, p: string) =>
      p.endsWith("/properties") ? Promise.resolve([{ id: 3, name: "Cottage" }]) : route(p),
    );
    render(Page);
    expect(await screen.findByRole("heading", { name: "Rental property tax" })).toBeTruthy();
    const property = (await screen.findByLabelText("Property")) as HTMLSelectElement;
    await vi.waitFor(() => expect(property.options.length).toBe(2));
    property.value = "3";
    property.dispatchEvent(new Event("change"));
    await vi.waitFor(() =>
      expect(screen.getByRole("link", { name: "Item 21 view" }).getAttribute("href")).toBe(
        "/acme/reports/rental?kind=item21&fy=2027&property_id=3",
      ),
    );
    expect(screen.getByRole("link", { name: "Accountant CSV" }).getAttribute("href")).toBe(
      "/api/v1/orgs/acme/rental-reports/accountant?fy=2027&property_id=3&format=csv",
    );
    expect(screen.getByRole("button", { name: "Item 21 labels" })).toBeTruthy();
  });
});
