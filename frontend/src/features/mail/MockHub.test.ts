// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { MockHub } from "./MockHub";

it("shows census before sync, applies unified filters, opens full threads, and posts scoped mutations", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const base = { id: 1, provider: "mock", account: "personal", native_id: "planning", thread_id: "project", folders: ["INBOX"], labels: ["项目"], subject: "项目计划", from: "sender@example.invalid", body: "你好", read: false, starred: true, oversized: false, attachments: [] };
  const sibling = { ...base, id: 2, native_id: "reply", subject: "Re: 项目计划", body: "確認しました", read: true, attachments: [{ id: 3, filename: "notes.txt", size: 20 }] };
  const oversized = { ...base, id: 3, native_id: "oversized", subject: "Large message", labels: null as unknown as string[], oversized: true, starred: false };
  const fetchMock = vi.fn(async (url: string, options?: RequestInit) => {
    if (options?.method === "POST") return { ok: true, json: async () => ({ ok: true }) };
    const params = new URL(url, "http://localhost").searchParams;
    return { ok: true, json: async () => ({ messages: params.get("view") === "trash" ? [] : params.get("view") === "all" ? [base, sibling, oversized] : [base, oversized], census: [{ account: { id: "personal" }, message_count: 4, estimated_raw_bytes: 53477376, expected_evidence_bytes: 53477376, recommended_free_disk: 80216064 }], real_credentials: 0, oauth_accounts: 0 }) };
  });
  vi.stubGlobal("fetch", fetchMock);
  const container = document.createElement("div"); document.body.appendChild(container);
  const root = createRoot(container);
  const navigate = vi.fn();
  const click = async (title: string) => {
    const button = [...container.querySelectorAll("button")].find(b => b.textContent === title);
    expect(button, title).toBeTruthy();
    await act(async () => { button!.click(); });
  };
  try {
    await act(async () => { root.render(createElement(MockHub, { csrf: "csrf-test", datePrefs: {} as any, swipePreferences: {} as any, navigate, addToast: vi.fn() })); });
    expect(container.textContent).toContain("Real credentials: 0 · OAuth: 0");
    expect(container.querySelector('[aria-label="Storage Census"]')?.textContent).toContain("53477376");
    expect(container.querySelector('[aria-label="Mail AI Side Chat"]')).toBeTruthy();
    expect(container.textContent).toContain("项目计划");
    expect(container.textContent).toContain("Large message");
    expect(container.textContent).toContain("Oversized stub");
    for (const title of ["Unified Inbox", "All Mail", "Unread", "Starred"]) expect(container.textContent).toContain(title);
    await click("Sync MockProvider");
    const sync = fetchMock.mock.calls.find(([url]) => url === "/api/mock-hub/sync")!;
    expect(sync[1]?.headers).toEqual({ "Content-Type": "application/json", "X-CSRF-Token": "csrf-test" });
    await click("Unread");
    expect(fetchMock.mock.calls.some(([url]) => url.includes("view=unread"))).toBe(true);
    await click("★ ● 项目计划");
    await click("Open thread and attachments");
    expect(navigate).toHaveBeenCalledWith("/messages/1?return=%2Fmock");
    await click("Mark read");
    const mutation = fetchMock.mock.calls.find(([url]) => url === "/api/mock-hub/mutate")!;
    expect(JSON.parse(mutation[1]!.body as string)).toEqual({ account: "personal", mutation: { message_id: "planning", action: "read", value: true, label: "" } });
    expect(container.querySelectorAll("select")).toHaveLength(2);
    expect(container.textContent).toContain("Label / folder");
  } finally {
    await act(async () => root.unmount()); container.remove(); vi.unstubAllGlobals();
  }
});
