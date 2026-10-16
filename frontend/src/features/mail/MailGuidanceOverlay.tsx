import { useEffect, useSyncExternalStore } from "react";
import { getMailSemanticAction, performMailSemanticAction, type MailSemanticMode } from "./semanticActions";

export type MailGuideStep = {
  action: string;
  title: string;
  detail: string;
  input?: unknown;
  cursor?: boolean;
};

type GuideState = {
  open: boolean;
  mode: MailSemanticMode;
  steps: MailGuideStep[];
  index: number;
  error: string;
};

let state: GuideState = { open: false, mode: "guide", steps: [], index: 0, error: "" };
const listeners = new Set<() => void>();
function publish(next: GuideState) {
  state = next;
  listeners.forEach((listener) => listener());
}
function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}function snapshot() { return state; }

export function startMailGuidance(mode: MailSemanticMode, steps: MailGuideStep[]) {
  publish({ open: steps.length > 0, mode, steps, index: 0, error: "" });
}

export function stopMailGuidance() {
  publish({ ...state, open: false, error: "" });
}

export function defaultMailGuide(mode: MailSemanticMode): MailGuideStep[] {
  return [
    {
      action: "mail.search",
      title: "搜索邮件",
      detail: mode === "guide" ? "在这里输入搜索条件。" : "Action Mode 会使用同一语义搜索动作。",
      input: { query: "is:unread" }, cursor: true
    },
    {
      action: "mail.filter.unread",
      title: "筛选未读",
      detail: mode === "guide" ? "使用语义动作切换到未读邮件。" : "授权后直接执行未读筛选。"
    },
    {
      action: "mail.compose", title: "写邮件", cursor: true,
      detail: mode === "guide" ? "这里打开写信窗口；发送仍需明确确认。" : "Action Mode 使用同一个 mail.compose 动作打开写信窗口。"
    }
  ];
}
export function MailGuidanceOverlay() {
  const guide = useSyncExternalStore(subscribe, snapshot, snapshot);
  const step = guide.steps[guide.index];

  useEffect(() => {
    if (!guide.open || !step) return;
    const target = getMailSemanticAction(step.action)?.target?.();
    target?.scrollIntoView({ block: "center", inline: "center", behavior: "smooth" });
  }, [guide.open, guide.index, step?.action]);

  if (!guide.open || !step) return null;
  const action = getMailSemanticAction(step.action);
  const target = action?.target?.() || null;
  const rect = target?.getBoundingClientRect();
  const spotlight = rect ? {
    left: Math.max(8, rect.left - 8),
    top: Math.max(8, rect.top - 8),
    width: Math.max(32, rect.width + 16),
    height: Math.max(32, rect.height + 16)
  } : null;

  async function execute() {
    if (!action) {
      publish({ ...state, error: `当前页面暂不提供 ${step.action}` });
      return;
    }
    try {
      await performMailSemanticAction(step.action, step.input, { approved: true });      const next = Math.min(guide.index + 1, guide.steps.length - 1);
      publish({ ...state, index: next, error: "", open: guide.index + 1 < guide.steps.length });
    } catch (err) {
      publish({ ...state, error: err instanceof Error ? err.message : "动作执行失败" });
    }
  }

  function move(delta: number) {
    const next = Math.max(0, Math.min(guide.steps.length - 1, guide.index + delta));
    publish({ ...state, index: next, error: "" });
  }

  return (
    <div className="mail-guide-layer" role="dialog" aria-label="Mail guidance">
      <div className="mail-guide-dim" />
      {spotlight ? <div className="mail-guide-spotlight" style={spotlight} /> : null}
      {spotlight && step.cursor ? (
        <div className="mail-guide-cursor" style={{ left: spotlight.left + spotlight.width - 12, top: spotlight.top + spotlight.height - 10 }}>➤</div>
      ) : null}
      <section className="mail-guide-bubble">
        <div className="mail-guide-head">
          <strong>{guide.mode === "guide" ? "Guide Mode" : "Action Mode"}</strong>
          <span>{guide.index + 1}/{guide.steps.length}</span>
          <button type="button" className="ghost" onClick={stopMailGuidance} aria-label="Close guide">×</button>
        </div>
        <h3>{step.title}</h3>
        <p>{step.detail}</p>        <code>{step.action}</code>
        {guide.error ? <div className="error" role="status">{guide.error}</div> : null}
        <div className="mail-guide-actions">
          <button type="button" className="secondary" disabled={guide.index === 0} onClick={() => move(-1)}>上一步</button>
          {guide.mode === "action" ? (
            <button type="button" onClick={() => void execute()} disabled={!action}>授权并执行本步</button>
          ) : (
            <button
              type="button"
              onClick={() => guide.index + 1 >= guide.steps.length ? stopMailGuidance() : move(1)}
            >
              {guide.index + 1 >= guide.steps.length ? "完成" : "下一步"}
            </button>
          )}
        </div>
      </section>
    </div>
  );
}
