import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AssistantRuntimeProvider,
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
type ThreadScope = "global" | "message" | "mail_thread";
type ThreadContext = {
  subject?: string;
  account?: string;
  message_id?: number;
  mail_thread_id?: string;
};
type ThreadSnapshot = {
  thread_id: string;
  scope: ThreadScope;
  context: ThreadContext;
  preview: string;
  message_count: number;
  messages: UIMessage[];
  created_at: string;
  updated_at: string;
};
type ThreadSummary = Omit<ThreadSnapshot, "messages">;

type ThreadListResponse = { threads: ThreadSummary[] };
type TraceEvent = {
  id: string;
  message_id: string;
  tool_call_id: string;
  tool_name: string;
  state: string;
  input?: unknown;
  output?: unknown;
  error_text?: string;
};
type TraceResponse = { thread_id: string; events: TraceEvent[] };

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

export function mailAIScopeLabel(scope: ThreadScope) {
  if (scope === "message") return "当前邮件";
  if (scope === "mail_thread") return "邮件线程";
  return "全部邮箱";
}

export function toolActivityLabel(name: string, args: unknown, result: unknown, running: boolean) {
  const label = TOOL_LABELS[name] || name;
  const record = (args && typeof args === "object") ? args as Record<string, unknown> : {};
  if (name === "search_mail" && typeof record.q === "string") return `${running ? "正在搜索" : "已搜索"}：${record.q}`;
  if (running) return `正在${label}`;
  if (Array.isArray(result)) return `${label}完成 · ${result.length} 条结果`;
  return `${label}完成`;
}

export function traceResultSummary(event: TraceEvent) {
  if (event.error_text) return `失败：${event.error_text}`;
  if (Array.isArray(event.output)) return `结果：${event.output.length} 条`;
  if (event.output && typeof event.output === "object") {
    const output = event.output as Record<string, unknown>;
    if (typeof output.subject === "string" && output.subject.trim()) return `结果：${output.subject}`;
    if (typeof output.ok === "boolean") return output.ok ? "结果：完成" : "结果：未完成";
    return `结果：${Object.keys(output).slice(0, 4).join("、") || "已返回"}`;
  }
  if (typeof event.output === "string" && event.output.trim()) {
    const text = event.output.trim();
    return `结果：${text.length > 96 ? `${text.slice(0, 96)}…` : text}`;
  }
  return event.state === "output-available" ? "结果：完成" : "等待结果";
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

function AssistantThread({ empty }: { empty: boolean }) {
  return <ThreadPrimitive.Root className="mail-ai-thread">
    <ThreadPrimitive.Viewport className="mail-ai-assistant-viewport">
      {empty ? <div className="mail-ai-empty">可以搜索三个邮箱、阅读线程、总结邮件或起草回复。工具执行过程会显示在这里。</div> : null}
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

function MailSideChatRuntime({ csrf, thread, onPersisted }: { csrf: string; thread: ThreadSnapshot; onPersisted: () => void }) {
  const transport = useMemo(() => new AssistantChatTransport({
    api: "/api/mail-ai/agent",
    headers: { "X-CSRF-Token": csrf },
    body: { thread_id: thread.thread_id }
  }), [csrf, thread.thread_id]);
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
        onPersisted();
      }).catch(() => console.error("Mail AI request failed without logging private content"));
    }
  });
  return <AssistantRuntimeProvider runtime={runtime}>
    <AssistantThread empty={thread.messages.length === 0} />
  </AssistantRuntimeProvider>;
}

function threadTitle(thread: ThreadSummary | ThreadSnapshot) {
  return thread.preview || thread.context?.subject || "新会话";
}

function threadTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString([], { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function MailSideChat({ csrf, messageID, defaultOpen = false }: { csrf: string; messageID: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [model, setModel] = useState("Agent");
  const [thread, setThread] = useState<ThreadSnapshot | null>(null);
  const [history, setHistory] = useState<ThreadSummary[]>([]);
  const [trace, setTrace] = useState<TraceEvent[]>([]);
  const [threadError, setThreadError] = useState("");
  const [showHistory, setShowHistory] = useState(false);
  const [showTrace, setShowTrace] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const loadHistory = useCallback(() => {
    void fetch("/api/mail-ai/threads", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Mail AI history failed (${response.status})`);
        return await response.json() as ThreadListResponse;
      })
      .then((value) => setHistory(Array.isArray(value.threads) ? value.threads : []))
      .catch(() => console.error("Mail AI request failed without logging private content"));
  }, []);

  const loadTrace = useCallback((threadID: string) => {
    void fetch(`/api/mail-ai/trace?thread_id=${encodeURIComponent(threadID)}`, { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Mail AI trace failed (${response.status})`);
        return await response.json() as TraceResponse;
      })
      .then((value) => setTrace(Array.isArray(value.events) ? value.events : []))
      .catch(() => console.error("Mail AI request failed without logging private content"));
  }, []);

  const loadThread = useCallback(async (url: string) => {
    setBusy(true);
    try {
      const response = await fetch(url, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`Mail AI history failed (${response.status})`);
      const value = await response.json() as ThreadSnapshot;
      if (!value.thread_id || !Array.isArray(value.messages)) throw new Error("Mail AI history response is invalid");
      setThread(value);
      setThreadError("");
      return value;
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/model-config", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => response.ok ? await response.json() as ModelConfig : null)
      .then((value) => { if (active && value?.model) setModel(value.model); })
      .catch(() => undefined);
    const params = new URLSearchParams({ scope: messageID > 0 ? "mail_thread" : "global" });
    if (messageID > 0) params.set("message_id", String(messageID));
    void loadThread(`/api/mail-ai/thread?${params.toString()}`)
      .then(() => { if (active) loadHistory(); })
      .catch((error) => { if (active) setThreadError(error instanceof Error ? error.message : "Mail AI history unavailable"); });
    return () => { active = false; };
  }, [loadHistory, loadThread, messageID]);

  const restoreThread = async (threadID: string) => {
    try {
      const restored = await loadThread(`/api/mail-ai/thread?thread_id=${encodeURIComponent(threadID)}`);
      if (showTrace) loadTrace(restored.thread_id);
      setShowHistory(false);
      setShowNew(false);
    } catch (error) {
      setThreadError(error instanceof Error ? error.message : "Mail AI history unavailable");
    }
  };

  const createThread = async (scope: ThreadScope) => {
    setBusy(true);
    try {
      const response = await fetch("/api/mail-ai/threads", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ scope, message_id: messageID })
      });
      if (!response.ok) throw new Error(`Mail AI new conversation failed (${response.status})`);
      const value = await response.json() as ThreadSnapshot;
      setThread(value);
      setTrace([]);
      setThreadError("");
      setShowNew(false);
      setShowHistory(false);
      setShowTrace(false);
      loadHistory();
    } catch (error) {
      setThreadError(error instanceof Error ? error.message : "Mail AI new conversation unavailable");
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)}>AI</button>;
  }
  const availableScopes: ThreadScope[] = messageID > 0 ? ["mail_thread", "message", "global"] : ["global"];
  return <aside className="mail-ai-panel mail-ai-panel-assistant" aria-label="Mail AI Side Chat">
    <button className="mail-ai-close ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
    <header className="mail-ai-assistant-header">
      <div className="mail-ai-header-title">
        <strong>Mail AI</strong>
        <span>{thread ? `${mailAIScopeLabel(thread.scope)} · ${thread.context?.subject || model}` : model}</span>
      </div>
      <div className="mail-ai-header-actions">
        <button type="button" className="ghost" disabled={busy} onClick={() => { setShowNew((value) => !value); setShowHistory(false); setShowTrace(false); }}>新建</button>
        <button type="button" className="ghost" disabled={busy} onClick={() => { setShowHistory((value) => !value); setShowNew(false); setShowTrace(false); loadHistory(); }}>历史</button>
        <button type="button" className="ghost" disabled={busy || !thread} onClick={() => {
          const next = !showTrace;
          setShowTrace(next);
          setShowHistory(false);
          setShowNew(false);
          if (next && thread) loadTrace(thread.thread_id);
        }}>过程</button>
      </div>
    </header>
    {showNew ? <section className="mail-ai-new-menu" aria-label="New Mail AI conversation">
      <strong>新建会话范围</strong>
      <div>{availableScopes.map((scope) => <button type="button" key={scope} disabled={busy} onClick={() => void createThread(scope)}>{mailAIScopeLabel(scope)}</button>)}</div>
    </section> : null}
    {showHistory ? <section className="mail-ai-history" aria-label="Mail AI conversation history">
      {history.length === 0 ? <div className="mail-ai-empty">还没有历史会话。</div> : history.map((item) => <button
        type="button"
        key={item.thread_id}
        className={`mail-ai-history-item${item.thread_id === thread?.thread_id ? " active" : ""}`}
        onClick={() => void restoreThread(item.thread_id)}
      >
        <span className="mail-ai-history-row"><strong>{mailAIScopeLabel(item.scope)}</strong><time>{threadTime(item.updated_at)}</time></span>
        <span className="mail-ai-history-title">{threadTitle(item)}</span>
      </button>)}
    </section> : null}
    {showTrace ? <section className="mail-ai-trace" aria-label="Mail AI work trace">
      <div className="mail-ai-trace-note">仅显示可审计的工具步骤和结果，不包含模型私有推理。</div>
      {trace.length === 0 ? <div className="mail-ai-empty">这个会话还没有工具步骤。</div> : trace.map((event, index) => <article className="mail-ai-trace-item" key={`${event.id}-${index}`}>
        <span className="mail-ai-trace-index">{index + 1}</span>
        <div>
          <strong>{toolActivityLabel(event.tool_name, event.input, event.output, event.state !== "output-available" && !event.error_text)}</strong>
          <span>{traceResultSummary(event)}</span>
        </div>
      </article>)}
    </section> : null}
    {threadError ? <div className="mail-ai-empty mail-ai-error">{threadError}</div> : null}
    {!thread && !threadError ? <div className="mail-ai-empty">正在恢复 Mail AI 对话…</div> : null}
    {thread ? <div className="mail-ai-runtime-shell" key={thread.thread_id}>
      <MailSideChatRuntime csrf={csrf} thread={thread} onPersisted={() => { loadHistory(); if (showTrace) loadTrace(thread.thread_id); }} />
    </div> : null}
  </aside>;
}
