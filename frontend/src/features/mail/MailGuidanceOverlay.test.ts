// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defaultMailGuide, MailGuidanceOverlay, startMailGuidance, stopMailGuidance } from "./MailGuidanceOverlay";
import { registerMailSemanticAction } from "./semanticActions";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot>;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => { stopMailGuidance(); root.unmount(); });
  container.remove();
  vi.unstubAllGlobals();
});

describe("Mail guidance", () => {
  it("reuses the same semantic actions in guide and action modes", () => {
    const guide = defaultMailGuide("guide");
    const action = defaultMailGuide("action");
    expect(guide.map((step) => step.action)).toEqual(action.map((step) => step.action));
    expect(guide.map((step) => step.action)).toEqual(["mail.search", "mail.filter.unread", "mail.compose"]);
    expect(guide.some((step) => step.cursor)).toBe(true);
  });
  it("shows spotlight guidance and advances without executing actions", () => {
    let executions = 0;
    const target = document.createElement("button");
    target.scrollIntoView = vi.fn();
    target.getBoundingClientRect = () => ({ x: 10, y: 20, left: 10, top: 20, right: 110, bottom: 60, width: 100, height: 40, toJSON: () => ({}) });
    document.body.appendChild(target);
    const disposeSearch = registerMailSemanticAction({ name: "mail.search", description: "Search", risk: "read", target: () => target, run: () => { executions += 1; } });
    const disposeUnread = registerMailSemanticAction({ name: "mail.filter.unread", description: "Unread", risk: "read", target: () => target, run: () => { executions += 1; } });
    try {
      act(() => root.render(createElement(MailGuidanceOverlay)));
      act(() => startMailGuidance("guide", defaultMailGuide("guide").slice(0, 2)));
      expect(container.textContent).toContain("Guide Mode");
      expect(container.textContent).toContain("1/2");
      expect(container.querySelector(".mail-guide-spotlight")).not.toBeNull();
      expect(container.querySelector(".mail-guide-cursor")).not.toBeNull();
      const next = [...container.querySelectorAll("button")].find((button) => button.textContent === "下一步");
      act(() => next?.click());
      expect(container.textContent).toContain("2/2");
      expect(executions).toBe(0);
    } finally {
      disposeSearch(); disposeUnread(); target.remove();
    }
  });
  it("executes the same semantic action only after Action Mode authorization", async () => {
    let executions = 0;
    const target = document.createElement("button");
    target.scrollIntoView = vi.fn();
    target.getBoundingClientRect = () => ({ x: 0, y: 0, left: 0, top: 0, right: 80, bottom: 30, width: 80, height: 30, toJSON: () => ({}) });
    document.body.appendChild(target);
    const dispose = registerMailSemanticAction({ name: "mail.search", description: "Search", risk: "read", target: () => target, run: () => { executions += 1; } });
    try {
      act(() => root.render(createElement(MailGuidanceOverlay)));
      act(() => startMailGuidance("action", defaultMailGuide("action").slice(0, 1)));
      expect(container.textContent).toContain("Action Mode");
      expect(executions).toBe(0);
      const run = [...container.querySelectorAll("button")].find((button) => button.textContent === "授权并执行本步");
      await act(async () => { run?.click(); await Promise.resolve(); });
      expect(executions).toBe(1);
      expect(container.textContent).not.toContain("Action Mode");
    } finally {
      dispose(); target.remove();
    }
  });
});