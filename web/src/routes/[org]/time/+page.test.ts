import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/svelte";

vi.mock("$app/state", () => ({
  page: { params: { org: "acme" }, url: new URL("http://localhost/acme/time") },
}));
const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock("../../../lib/api", () => ({
  api: apiMock,
  ApiError: class ApiError extends Error {},
}));
vi.mock("../../../lib/session.svelte", () => ({
  session: { currentOrg: { perms: ["timesheets_read", "timesheets_write"] } },
}));

import Page from "./+page.svelte";

const ENTRIES = {
  entries: [
    {
      id: 5,
      task_id: 11,
      work_date: "2026-11-06",
      minutes: 90,
      note: "design",
    },
    {
      id: 6,
      task_id: 11,
      work_date: "2026-11-05",
      minutes: 30,
      note: "",
      invoice_line_id: 1,
    },
  ],
  total_minutes: 120,
};

function route(method: string, path: string) {
  if (method === "GET" && path.endsWith("/projects?active=true"))
    return Promise.resolve([
      { id: 3, name: "Website", client_contact_id: 2, hourly_rate: 15000 },
    ]);
  if (method === "GET" && path.endsWith("/projects/3/tasks"))
    return Promise.resolve([{ id: 11, name: "Build", active: true }]);
  if (method === "GET" && path.includes("/time-entries?project_id=3"))
    return Promise.resolve(ENTRIES);
  return Promise.resolve({});
}

describe("time page", () => {
  beforeEach(() => {
    apiMock.mockReset();
    apiMock.mockImplementation(route);
  });

  it("lists entries, marks billed ones, and hides admin for non-admins", async () => {
    render(Page);
    expect(await screen.findByText("design")).toBeTruthy();
    expect(screen.getByText("Entries (2h 00m)")).toBeTruthy();
    expect(screen.getByText("Billed")).toBeTruthy();
    expect(screen.getAllByText("Delete")).toHaveLength(1);
    expect(screen.queryByText("Projects and tasks")).toBeNull();
  });

  it("logs time against the selected task", async () => {
    render(Page);
    await screen.findByText("design");
    await fireEvent.click(screen.getByRole("button", { name: "Log time" }));
    await vi.waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        "POST",
        "/api/v1/orgs/acme/time-entries",
        {
          task_id: 11,
          work_date: expect.any(String),
          minutes: 60,
          note: undefined,
        },
      ),
    );
  });
});
