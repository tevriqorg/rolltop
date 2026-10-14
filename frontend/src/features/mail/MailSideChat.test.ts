import { describe, expect, it } from "vitest";
import { parseMailAIFrames } from "./MailSideChat";

describe("parseMailAIFrames", () => {
  it("keeps incomplete SSE frames and exposes tool activity", () => {
    const first = parseMailAIFrames('data: {"type":"tool_call","tool":"search_mail"}\n\ndata: {"type":"answer_delta","text":"hel');
    expect(first.events).toEqual([{ type: "tool_call", tool: "search_mail" }]);
    expect(first.rest).toContain('answer_delta');
    const second = parseMailAIFrames(first.rest + 'lo"}\n\n');
    expect(second.events).toEqual([{ type: "answer_delta", text: "hello" }]);
  });
});
