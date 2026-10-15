import http from 'node:http';
import { Readable } from 'node:stream';
import { createOpenAICompatible } from '@ai-sdk/openai-compatible';
import { ToolLoopAgent, createAgentUIStreamResponse, isStepCount, tool } from 'ai';
import { z } from 'zod';
import { assertMailAIWorkspaceTools, mailAIWorkspaceInstructions } from './workspace.mjs';

const addr = process.env.MAIL_AI_ADDR || '127.0.0.1:18091';
const [host, portText] = addr.split(':');
const port = Number(portText || '18091');
const hubURL = (process.env.MAIL_AI_HUB_URL || 'http://127.0.0.1:18090').replace(/\/$/, '');
const shared = process.env.MAIL_AI_SHARED_TOKEN || '';
let modelConfig = {
  baseURL: (process.env.MAIL_AI_MODEL_BASE_URL || '').replace(/\/$/, ''),
  model: process.env.MAIL_AI_MODEL || '',
  apiKey: process.env.MAIL_AI_MODEL_API_KEY || '',
};

function authorized(req) {
  return shared && req.headers.authorization === `Bearer ${shared}`;
}
async function readJSON(req, limit = 512 * 1024) {
  const chunks = []; let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new Error('request too large');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
}

function latestUserText(messages = []) {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m?.role !== 'user') continue;
    if (typeof m.content === 'string') return m.content.trim();
    if (Array.isArray(m.parts)) {
      return m.parts.filter(p => p?.type === 'text').map(p => p.text || '').join('\n').trim();
    }
  }
  return '';
}

async function hubTool(userID, name, args, confirmed = false) {
  const response = await fetch(`${hubURL}/api/mail-ai/tools`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${shared}` },
    body: JSON.stringify({ user_id: userID, name, arguments: args, ...(confirmed ? { confirmed: true } : {}) }),
  });
  const text = await response.text();
  let payload;
  try { payload = JSON.parse(text); } catch { payload = { error: text || `HTTP ${response.status}` }; }
  if (!response.ok) return { ok: false, status: response.status, error: payload?.error || text };
  return payload?.result ?? payload;
}

function createTools({ userID, context, fallbackQuery }) {
  return assertMailAIWorkspaceTools({
    list_mail: tool({
      description: 'List recent mail metadata across connected mailboxes without loading message bodies. Use this first for broad mailbox overview questions, deadlines, conflicts, action items, or when search terms are unknown.',
      inputSchema: z.object({ account: z.string().trim().optional(), limit: z.number().int().min(1).max(20).optional() }),
      execute: async ({ account, limit }) => hubTool(userID, 'list_mail', { ...(account ? { account } : {}), limit: limit || 20 }),
    }),
    search_mail: tool({
      description: 'Search all connected mailboxes in Rolltop. Use this for cross-account lookup.',
      inputSchema: z.object({ q: z.string().trim().optional() }),
      execute: async ({ q }) => hubTool(userID, 'search_mail', { q: q || fallbackQuery || context.subject || 'mail' }),
    }),
    get_message: tool({
      description: 'Read one message from the local Rolltop mirror.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'get_message', { message_id: message_id || context.message_id }),
    }),
    get_thread: tool({
      description: 'Read the current or named provider thread from Rolltop.',
      inputSchema: z.object({ account: z.string().trim().optional(), thread_id: z.string().trim().optional() }),
      execute: async ({ account, thread_id }) => hubTool(userID, 'get_thread', {
        account: account || context.account,
        thread_id: thread_id || context.thread_id,
      }),
    }),
    draft_reply: tool({
      description: 'Create reply draft text only. This never sends email.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), body: z.string().trim().min(1) }),
      execute: async ({ message_id, body }) => hubTool(userID, 'draft_reply', { message_id: message_id || context.message_id, body }),
    }),
    add_label: tool({
      description: 'Add a label to a message. This changes mail state and requires user approval.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), label: z.string().trim().min(1) }),
      execute: async ({ message_id, label }) => hubTool(userID, 'add_label', { message_id: message_id || context.message_id, label }, true),
    }),
    archive_message: tool({
      description: 'Archive a message. This changes mail state and requires user approval.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'archive_message', { message_id: message_id || context.message_id }, true),
    }),
    mark_read: tool({
      description: 'Set message read state. This changes mail state and requires user approval.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), read: z.boolean() }),
      execute: async ({ message_id, read }) => hubTool(userID, 'mark_read', { message_id: message_id || context.message_id, read }, true),
    }),
  });
}

function createAgent(body) {
  const provider = createOpenAICompatible({
    baseURL: modelConfig.baseURL,
    name: 'vela-mail-ai',
    ...(modelConfig.apiKey ? { apiKey: modelConfig.apiKey } : {}),
  });
  const context = body.context || {};
  const fallbackQuery = latestUserText(body.messages || []);
  const tools = createTools({ userID: body.user_id, context, fallbackQuery });
  return new ToolLoopAgent({
    model: provider.chatModel(modelConfig.model),
    stopWhen: isStepCount(6),
    instructions: mailAIWorkspaceInstructions(context),
    tools,
    toolApproval: { add_label: 'user-approval', archive_message: 'user-approval', mark_read: 'user-approval' },
  });
}
async function sendWebResponse(webResponse, res) {
  res.statusCode = webResponse.status;
  for (const [key, value] of webResponse.headers) res.setHeader(key, value);
  if (!webResponse.body) return res.end();
  Readable.fromWeb(webResponse.body).pipe(res);
}

async function validateConfig(next) {
  const baseURL = String(next.base_url || '').replace(/\/$/, '');
  const model = String(next.model || '').trim();
  if (!baseURL || !model) throw new Error('base_url and model are required');
  const headers = next.api_key ? { authorization: `Bearer ${next.api_key}` } : {};
  const response = await fetch(`${baseURL}/models`, { headers, redirect: 'manual' });
  if (!response.ok) throw new Error(`model endpoint /models returned HTTP ${response.status}`);
  const payload = await response.json();
  const ids = Array.isArray(payload?.data) ? payload.data.map(x => String(x?.id || '')) : [];
  if (!ids.includes(model)) throw new Error(`model ${model} was not returned by /models`);
  modelConfig = { baseURL, model, apiKey: next.clear_api_key ? '' : (next.api_key ?? modelConfig.apiKey) };
}

function publicConfig() {
  return { configured: Boolean(modelConfig.baseURL && modelConfig.model), base_url: modelConfig.baseURL, model: modelConfig.model, api_key_configured: Boolean(modelConfig.apiKey) };
}
const server = http.createServer(async (req, res) => {
  try {
    if (req.url === '/healthz' && req.method === 'GET') {
      res.setHeader('content-type', 'application/json');
      return res.end(JSON.stringify({ ok: true, configured: publicConfig().configured, model: modelConfig.model, runtime: 'ai-sdk-tool-loop-agent' }));
    }
    if (!authorized(req)) { res.statusCode = 401; return res.end('unauthorized'); }
    if (req.url === '/config' && req.method === 'GET') {
      res.setHeader('content-type', 'application/json'); return res.end(JSON.stringify(publicConfig()));
    }
    if (req.url === '/config' && req.method === 'PUT') {
      const body = await readJSON(req, 32 * 1024); await validateConfig(body);
      res.setHeader('content-type', 'application/json'); return res.end(JSON.stringify(publicConfig()));
    }
    if (req.url !== '/agent' || req.method !== 'POST') { res.statusCode = 404; return res.end('not found'); }
    const body = await readJSON(req);
    if (!Number.isInteger(body.user_id) || body.user_id <= 0 || !body.context?.mode || !Array.isArray(body.messages)) {
      res.statusCode = 400; return res.end('invalid agent request');
    }
    if (!publicConfig().configured) { res.statusCode = 503; return res.end('model is not configured'); }
    const agent = createAgent(body);
    const response = await createAgentUIStreamResponse({ agent, uiMessages: body.messages, keepAliveMs: 15000 });
    return await sendWebResponse(response, res);
  } catch (error) {
    const errorType = error?.constructor?.name || typeof error;
    const status = Number.isInteger(error?.status) ? ` status=${error.status}` : '';
    const code = typeof error?.code === 'string' && /^[A-Za-z0-9_.-]{1,64}$/.test(error.code) ? ` code=${error.code}` : '';
    console.error(`mail-ai-agent request failed type=${errorType}${status}${code}`);
    if (!res.headersSent) res.statusCode = 500;
    if (!res.writableEnded) res.end('mail-ai agent request failed');
  }
});

server.listen(port, host, () => console.log(`mail-ai-agent listening on ${host}:${port}`));
