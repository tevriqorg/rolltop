import { describe, expect, it } from "vitest";
import { listMailSemanticActions, performMailSemanticAction, registerMailSemanticAction } from "./semanticActions";

describe("Mail semantic frontend actions", () => {
  it("uses stable semantic names and does not require selectors", async () => {
    const seen: unknown[] = [];
    const dispose = registerMailSemanticAction({ name: "mail.search", description: "Search", risk: "read", run: (input) => { seen.push(input); } });
    try {
      expect(listMailSemanticActions()).toContainEqual({ name: "mail.search", description: "Search", risk: "read" });
      await performMailSemanticAction("mail.search", { query: "is:unread" });
      expect(seen).toEqual([{ query: "is:unread" }]);
    } finally { dispose(); }
  });

  it("requires explicit approval before high-risk semantic actions", async () => {
    let sends = 0;
    const dispose = registerMailSemanticAction({ name: "compose.send", description: "Send", risk: "high", run: () => { sends += 1; } });
    try {
      await expect(performMailSemanticAction("compose.send")).rejects.toThrow(/explicit approval/);
      expect(sends).toBe(0);
      await performMailSemanticAction("compose.send", undefined, { approved: true });
      expect(sends).toBe(1);
    } finally { dispose(); }
  });
});