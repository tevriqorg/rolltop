import { useEffect, useMemo, useState } from "react";
import {
  AssistantRuntimeProvider,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  type TextMessagePartProps,
  type ToolCallMessagePartProps
} from "@assistant-ui/react";
import { AssistantChatTransport, useChatRuntime } from "@assistant-ui/ai-sdk";
import { lastAssistantMessageIsCompleteWithApprovalResponses } from "ai";

type ModelConfig = { configured: boolean; model?: string };

const TOOL_LABELS: Record<string, string> = {
  search_mail: "搜索邮件",
  get_message: "读取邮件",
  get_thread: "读取邮件线程",
  draft_reply: "生成回复草稿",
  add_label: "添加标签",
  archive_message: "归档邮件",
  mark_read: "修改已读状态"
};

export function toolActivityLabel(name: string, args: unknown, result: unknown, running: boolean) {
  const label = TOOL_LABELS[name] || name;
  const record = (args && typeof args === "object") ? args as Record<string, unknown> : {};
  if (name === "search_mail" && typeof record.q === "string") return `${running ? "正在搜索" : "已搜索"}：${record.q}`;
  if (running) return `正在${label}`;
  if (Array.isArray(result)) return `${label}完成 · ${result.length} 条结果`;
  return `${label}完成`;
}
function TextPart({ text }: TextMessagePartProps) {
  return <p className="mail-ai-text">{text}</p>;
}

function ToolCard(props: ToolCallMessagePartProps) {
  const { toolName, args, result, status, approval, respondToApproval, isError } = props;
  const running = status?.type === "running" || result === undefined;
  const pending = approval && approval.approved === undefined && !approval.resolution;
  return <section className={`mail-ai-tool-card${isError ? " error" : ""}`}>
    <div className="mail-ai-tool-title">
      <span className="mail-ai-tool-dot" aria-hidden="true" />
      <strong>{toolActivityLabel(toolName, args, result, running)}</strong>
    </div>
    {toolName === "draft_reply" && result ? <pre className="mail-ai-tool-result">{JSON.stringify(result, null, 2)}</pre> : null}
    {pending ? <div className="mail-ai-approval">
      <span>这一步会修改邮件状态，需要你的确认。</span>
      <div>
        <button type="button" onClick={() => void respondToApproval({ approved: true })}>允许</button>
        <button type="button" className="secondary" onClick={() => void respondToApproval({ approved: false, reason: "User declined" })}>拒绝</button>
      </div>
    </div> : null}
  </section>;
}

function UserMessage() {
  return <MessagePrimitive.Root className="mail-ai-message user">
    <MessagePrimitive.Parts components={{ Text: TextPart }} />
  </MessagePrimitive.Root>;
}

function AssistantMessage() {
  return <MessagePrimitive.Root className="mail-ai-message assistant">
    <MessagePrimitive.Parts components={{ Text: TextPart, Reasoning: () => <div className="mail-ai-thinking">正在分析…</div>, tools: { Fallback: ToolCard } }} />
  </MessagePrimitive.Root>;
}
function AssistantThread({ model }: { model: string }) {
  return <ThreadPrimitive.Root className="mail-ai-thread">
    <header className="mail-ai-assistant-header">
      <div><strong>Mail AI</strong><span>{model || "Agent"}</span></div>
      <span className="mail-ai-runtime-badge">ToolLoopAgent</span>
    </header>
    <ThreadPrimitive.Viewport className="mail-ai-assistant-viewport">
      <div className="mail-ai-empty">可以搜索三个邮箱、阅读线程、总结邮件或起草回复。工具执行过程会显示在这里。</div>
      <ThreadPrimitive.Messages components={{ UserMessage, AssistantMessage }} />
      <ThreadPrimitive.ViewportFooter className="mail-ai-assistant-footer">
        <ComposerPrimitive.Root className="mail-ai-composer">
          <ComposerPrimitive.Input placeholder="问这封邮件，或搜索全部邮箱…" rows={3} />
          <ComposerPrimitive.Send>发送</ComposerPrimitive.Send>
        </ComposerPrimitive.Root>
      </ThreadPrimitive.ViewportFooter>
    </ThreadPrimitive.Viewport>
  </ThreadPrimitive.Root>;
}

export function MailSideChat({ csrf, messageID, defaultOpen = false }: { csrf: string; messageID: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [model, setModel] = useState("Agent");
  const transport = useMemo(() => new AssistantChatTransport({
    api: "/api/mail-ai/agent",
    headers: { "X-CSRF-Token": csrf },
    prepareSendMessagesRequest: async ({ body }) => ({ body: { ...body, message_id: messageID } })
  }), [csrf, messageID]);
  const runtime = useChatRuntime({
    id: `mail-${messageID}`,
    transport,
    sendAutomaticallyWhen: lastAssistantMessageIsCompleteWithApprovalResponses
  });
  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/model-config", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => response.ok ? await response.json() as ModelConfig : null)
      .then((value) => { if (active && value?.model) setModel(value.model); })
      .catch(() => undefined);
    return () => { active = false; };
  }, []);

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)} disabled={messageID <= 0}>AI</button>;
  }
  return <aside className="mail-ai-panel mail-ai-panel-assistant" aria-label="Mail AI Side Chat">
    <button className="mail-ai-close ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
    <AssistantRuntimeProvider runtime={runtime}>
      <AssistantThread model={model} />
    </AssistantRuntimeProvider>
  </aside>;
}
