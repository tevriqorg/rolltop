import { describe, expect, it } from "vitest";
import { mailAIScopeLabel, mailAIWorkspaceAvailableScopes, mailAIWorkspaceInitialScope, latestAssistantVisibleText, mailAIWorkflowTimeline, mailAITimelinePhase, mailToolCardKind, mailToolCardSummary, toolActivityLabel, traceResultSummary } from "./MailSideChat";

describe("MailSideChat tool activity", () => {
  it("shows useful mail-search progress instead of raw tool JSON", () => {
    expect(toolActivityLabel("search_mail", { q: "项目计划" }, undefined, true)).toBe("正在搜索：项目计划");
    expect(toolActivityLabel("search_mail", { q: "项目计划" }, [{ id: 1 }, { id: 2 }], false)).toBe("已搜索：项目计划");
  });

  it("summarizes completed tool result counts", () => {
    expect(toolActivityLabel("get_thread", {}, [{ id: 1 }, { id: 2 }], false)).toBe("读取邮件线程完成 · 2 条结果");
  });

  it("maps mail tools to dedicated readable card types", () => {
    expect(mailToolCardKind("search_mail")).toBe("mail-list");
    expect(mailToolCardKind("get_message")).toBe("message");
    expect(mailToolCardKind("get_thread")).toBe("thread");
    expect(mailToolCardKind("draft_reply")).toBe("draft");
    expect(mailToolCardKind("archive_message")).toBe("mutation");
    expect(mailToolCardSummary("search_mail", [{ subject: "A" }, { subject: "B" }])).toBe("2 封邮件");
    expect(mailToolCardSummary("draft_reply", { subject: "Re: 项目" })).toBe("Re: 项目");
    expect(mailToolCardSummary("archive_message", { ok: true })).toBe("已完成");
  });

  it("labels persisted conversation scopes distinctly", () => {
    expect(mailAIScopeLabel("global")).toBe("全部邮箱");
    expect(mailAIScopeLabel("message")).toBe("当前邮件");
    expect(mailAIScopeLabel("mail_thread")).toBe("邮件线程");
  });

  it("enters the Agent Workspace in the correct page context", () => {
    expect(mailAIWorkspaceInitialScope(0)).toBe("global");
    expect(mailAIWorkspaceAvailableScopes(0)).toEqual(["global"]);
    expect(mailAIWorkspaceInitialScope(42)).toBe("mail_thread");
    expect(mailAIWorkspaceAvailableScopes(42)).toEqual(["mail_thread", "message", "global"]);
  });

  it("builds a continuous auditable Tool Timeline without private reasoning", () => {
    expect(mailAITimelinePhase("list_mail")).toBe("浏览");
    expect(mailAITimelinePhase("search_mail")).toBe("搜索");
    expect(mailAITimelinePhase("get_message")).toBe("读取");
    expect(mailAITimelinePhase("archive_message")).toBe("动作");
    const events = [
      { id: "a", message_id: "m", tool_call_id: "a", tool_name: "list_mail", state: "output-available", output: [{ message_id: 1 }, { message_id: 2 }] },
      { id: "b", message_id: "m", tool_call_id: "b", tool_name: "get_message", state: "output-available", output: { message_id: 1, subject: "A" } },
      { id: "c", message_id: "m", tool_call_id: "c", tool_name: "get_message", state: "output-available", output: { message_id: 2, subject: "B" } },
      { id: "d", message_id: "m", tool_call_id: "d", tool_name: "archive_message", state: "output-available", output: { ok: true } }
    ];
    const messages = [{ id: "assistant-1", role: "assistant", parts: [{ type: "reasoning", text: "secret" }, { type: "text", text: "这是最终可见结论" }] }] as never;
    expect(latestAssistantVisibleText(messages)).toBe("这是最终可见结论");
    const timeline = mailAIWorkflowTimeline(events, messages);
    expect(timeline.map((item) => item.phase)).toEqual(["浏览", "读取", "读取", "比较", "动作", "结论"]);
    expect(timeline.map((item) => item.detail).join(" ")).not.toContain("secret");
  });

  it("summarizes auditable trace results without requiring reasoning text", () => {
    expect(traceResultSummary({ id: "1", message_id: "a", tool_call_id: "t", tool_name: "search_mail", state: "output-available", output: [{ id: 1 }, { id: 2 }] })).toBe("结果：2 条");
    expect(traceResultSummary({ id: "2", message_id: "a", tool_call_id: "t", tool_name: "get_message", state: "output-available", output: { subject: "项目进展" } })).toBe("结果：项目进展");
  });
});
