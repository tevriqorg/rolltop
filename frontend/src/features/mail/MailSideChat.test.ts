import { describe, expect, it } from "vitest";
import { mailAIScopeLabel, mailAIWorkspaceAvailableScopes, mailAIWorkspaceInitialScope, mailToolCardKind, mailToolCardSummary, toolActivityLabel, traceResultSummary } from "./MailSideChat";

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

  it("summarizes auditable trace results without requiring reasoning text", () => {
    expect(traceResultSummary({ id: "1", message_id: "a", tool_call_id: "t", tool_name: "search_mail", state: "output-available", output: [{ id: 1 }, { id: 2 }] })).toBe("结果：2 条");
    expect(traceResultSummary({ id: "2", message_id: "a", tool_call_id: "t", tool_name: "get_message", state: "output-available", output: { subject: "项目进展" } })).toBe("结果：项目进展");
  });
});
