import assert from 'node:assert/strict';
import test from 'node:test';
import { mailAIToolApprovalStatus, mailAIToolRisk } from './approval-policy.mjs';
import {
  MAIL_AI_WORKSPACE_TOOLS,
  assertMailAIWorkspaceTools,
  mailAIWorkspaceInstructions,
  mailAIWorkspaceMode,
} from './workspace.mjs';

const fullTools = Object.fromEntries(MAIL_AI_WORKSPACE_TOOLS.map((name) => [name, {}]));

test('Agent Workspace maps global, message, and mail-thread contexts', () => {
  assert.equal(mailAIWorkspaceMode({}), 'global');
  assert.equal(mailAIWorkspaceMode({ mode: 'message' }), 'message');
  assert.equal(mailAIWorkspaceMode({ mode: 'message', scope: 'mail_thread' }), 'mail_thread');
});

test('Agent Workspace requires the complete mail tool catalog', () => {
  assert.equal(assertMailAIWorkspaceTools(fullTools), fullTools);
  assert.throws(() => assertMailAIWorkspaceTools({ search_mail: {} }), /tool contract mismatch/);
});


test('Agent Workspace exposes the complete A9 mail action catalog', () => {
  for (const name of ['star', 'unstar', 'move', 'snooze', 'trash', 'restore', 'create_draft', 'reply_mail', 'forward_mail', 'send_mail']) {
    assert.ok(MAIL_AI_WORKSPACE_TOOLS.includes(name), `missing A9 tool ${name}`);
  }
});


test('A10 risk-tiered approval policy is conservative by default', () => {
  for (const name of ['list_mail', 'search_mail', 'get_message', 'get_thread', 'list_mailboxes', 'draft_reply', 'create_draft']) {
    assert.equal(mailAIToolRisk(name), 'automatic');
    assert.equal(mailAIToolApprovalStatus(name), undefined);
  }
  for (const name of ['star', 'unstar', 'move', 'snooze', 'restore', 'add_label', 'archive_message', 'mark_read']) {
    assert.equal(mailAIToolRisk(name), 'review');
    assert.equal(mailAIToolApprovalStatus(name), 'user-approval');
    assert.equal(mailAIToolApprovalStatus(name, 'automatic'), undefined);
  }
  for (const name of ['trash', 'reply_mail', 'forward_mail', 'send_mail']) {
    assert.equal(mailAIToolRisk(name), 'high');
    assert.equal(mailAIToolApprovalStatus(name), 'user-approval');
    assert.equal(mailAIToolApprovalStatus(name, 'automatic'), 'user-approval');
  }
});
