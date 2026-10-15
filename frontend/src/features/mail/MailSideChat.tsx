import { useEffect, useMemo, useState } from "react";
import {
  ActionBarPrimitive,
  AssistantRuntimeProvider,
  BranchPickerPrimitive,
  ComposerPrimitive,
  MessagePrimitive,
  ThreadPrimitive,
  type TextMessagePartProps,
  type ToolCallMessagePartProps
} from "@assistant-ui/react";
import { AssistantChatTransport, useChatRuntime } from "@assistant-ui/ai-sdk";
import { lastAssistantMessageIsCompleteWithApprovalResponses, type UIMessage } from "ai";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

type ModelConfig = { configured: boolean; model?: string };

const TOOL_LABELS: Record<string, string> = {
  list_mail: "浏览邮箱",
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
function UserTextPart({ text }: TextMessagePartProps) {
  return <p className="mail-ai-text">{text}</p>;
}

function AssistantMarkdownPart({ text }: TextMessagePartProps) {
  return <div className="mail-ai-markdown">
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ({ href, children }) => <a href={href} target="_blank" rel="noreferrer">{children}</a>
      }}
    >{text}</ReactMarkdown>
  </div>;
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
    <MessagePrimitive.Parts components={{ Text: UserTextPart }} />
  </MessagePrimitive.Root>;
}

function AssistantMessage() {
  return <MessagePrimitive.Root className="mail-ai-message assistant">
    <MessagePrimitive.Parts components={{ Text: AssistantMarkdownPart, Reasoning: () => <div className="mail-ai-thinking">正在分析…</div>, tools: { Fallback: ToolCard } }} />
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
          <div className="mail-ai-composer-actions"><ComposerPrimitive.Cancel>停止</ComposerPrimitive.Cancel><ComposerPrimitive.Send>发送</ComposerPrimitive.Send></div>
        </ComposerPrimitive.Root>
      </ThreadPrimitive.ViewportFooter>
    </ThreadPrimitive.Viewport>
  </ThreadPrimitive.Root>;
}

type ThreadSnapshot = {
  thread_id: string;
  messages: UIMessage[];
};

function MailSideChatRuntime({ csrf, messageID, thread, model }: { csrf: string; messageID: number; thread: ThreadSnapshot; model: string }) {
  const transport = useMemo(() => new AssistantChatTransport({
    api: "/api/mail-ai/agent",
    headers: { "X-CSRF-Token": csrf },
    body: { message_id: messageID, thread_id: thread.thread_id }
  }), [csrf, messageID, thread.thread_id]);
  const runtime = useChatRuntime<UIMessage>({
    messages: thread.messages,
    transport,
    sendAutomaticallyWhen: lastAssistantMessageIsCompleteWithApprovalResponses,
    onFinish: ({ messages, isAbort, isDisconnect, isError }) => {
      if (isAbort || isDisconnect || isError) return;
      void fetch("/api/mail-ai/thread", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ thread_id: thread.thread_id, messages })
      }).then((response) => {
        if (!response.ok) throw new Error(`Mail AI persistence failed (${response.status})`);
      }).catch((error) => console.error(error));
    }
  });
  return <AssistantRuntimeProvider runtime={runtime}>
    <AssistantThread model={model} />
  </AssistantRuntimeProvider>;
}

export function MailSideChat({ csrf, messageID, defaultOpen = false }: { csrf: string; messageID: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [model, setModel] = useState("Agent");
  const [thread, setThread] = useState<ThreadSnapshot | null>(null);
  const [threadError, setThreadError] = useState("");
  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/model-config", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => response.ok ? await response.json() as ModelConfig : null)
      .then((value) => { if (active && value?.model) setModel(value.model); })
      .catch(() => undefined);
    void fetch("/api/mail-ai/thread", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Mail AI history failed (${response.status})`);
        return await response.json() as ThreadSnapshot;
      })
      .then((value) => {
        if (!active) return;
        if (!value.thread_id || !Array.isArray(value.messages)) throw new Error("Mail AI history response is invalid");
        setThread(value);
        setThreadError("");
      })
      .catch((error) => {
        if (active) setThreadError(error instanceof Error ? error.message : "Mail AI history unavailable");
      });
    return () => { active = false; };
  }, []);

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)}>AI</button>;
  }
  return <aside className="mail-ai-panel mail-ai-panel-assistant" aria-label="Mail AI Side Chat">
    <button className="mail-ai-close ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
    {threadError ? <div className="mail-ai-empty">{threadError}</div> : null}
    {!thread && !threadError ? <div className="mail-ai-empty">正在恢复 Mail AI 对话…</div> : null}
    {thread ? <MailSideChatRuntime csrf={csrf} messageID={messageID} thread={thread} model={model} /> : null}
  </aside>;
}
