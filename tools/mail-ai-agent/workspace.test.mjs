import assert from 'node:assert/strict';
import test from 'node:test';
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
