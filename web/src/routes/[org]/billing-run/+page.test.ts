import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";

vi.mock("$app/state", () => ({
  page: {
    params: { org: "acme" },
    url: new URL("http://localhost/acme/billing-run"),
  },
}));
const { apiMock, gotoMock } = vi.hoisted(() => ({
  apiMock: vi.fn(),
  gotoMock: vi.fn(),
}));
vi.mock("$app/navigation", () => ({ goto: gotoMock }));
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

const CONTACTS = [
  { id: 1, name: "Tenant Tim", kind: "tenant" },
  { id: 2, name: "Client Co", kind: "client" },
];

describe("billing run page", () => {
  beforeEach(() => {
    apiMock.mockReset();
    gotoMock.mockReset();
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 10, 7));
  });

  it("posts the run for a client and opens the invoice", async () => {
    apiMock.mockImplementation((m: string) =>
      m === "GET"
        ? Promise.resolve(CONTACTS)
        : Promise.resolve({
            invoice: { id: 77 },
            entry_count: 3,
            total_minutes: 180,
          }),
    );
    render(Page);
    expect(await screen.findByText("Client Co")).toBeTruthy();
    expect(screen.queryByText("Tenant Tim")).toBeNull();
    await fireEvent.click(screen.getByText("Create invoice"));
    await vi.waitFor(() =>
      expect(gotoMock).toHaveBeenCalledWith("/acme/invoices/77"),
    );
    expect(apiMock).toHaveBeenCalledWith(
      "POST",
      "/api/v1/orgs/acme/billing-runs",
      {
        client_contact_id: 2,
        from: "2026-11-01",
        to: "2026-11-07",
        due_in_days: 14,
        issue: false,
      },
    );
  });

  it("shows a run error", async () => {
    apiMock.mockImplementation((m: string) =>
      m === "GET"
        ? Promise.resolve(CONTACTS)
        : Promise.reject(new ApiError(422, "no unbilled entries in range")),
    );
    render(Page);
    await screen.findByText("Client Co");
    await fireEvent.click(screen.getByText("Create invoice"));
    expect(
      await screen.findByText("no unbilled entries in range"),
    ).toBeTruthy();
  });
});
