import { describe, expect, it } from "vitest";
import { toolActivityLabel } from "./MailSideChat";

describe("MailSideChat tool activity", () => {
  it("shows useful mail-search progress instead of raw tool JSON", () => {
    expect(toolActivityLabel("search_mail", { q: "项目计划" }, undefined, true)).toBe("正在搜索：项目计划");
    expect(toolActivityLabel("search_mail", { q: "项目计划" }, [{ id: 1 }, { id: 2 }], false)).toBe("已搜索：项目计划");
  });

  it("summarizes completed tool result counts", () => {
    expect(toolActivityLabel("get_thread", {}, [{ id: 1 }, { id: 2 }], false)).toBe("读取邮件线程完成 · 2 条结果");
  });
});
