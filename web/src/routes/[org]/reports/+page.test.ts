import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/svelte";

vi.mock("$app/state", () => ({
  page: {
    params: { org: "acme" },
    url: new URL("http://localhost/acme/reports"),
  },
}));

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock("../../../lib/api", () => ({
  api: apiMock,
  ApiError: class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
}));

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
    apiMock.mockReset();
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
});
