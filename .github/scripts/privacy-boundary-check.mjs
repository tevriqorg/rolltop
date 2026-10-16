import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const root = resolve(import.meta.dirname, '../..');
const failures = [];
const tracked = execFileSync('git', ['ls-files', '-z'], {
  cwd: root,
  encoding: 'utf8',
}).split('\0').filter(Boolean);

const allowedEnvFiles = new Set(['.env.example', '.env.sample']);
const privatePath = /(^|\/)(data|runtime|\.runtime|private-runtime|secrets?)(\/|$)/i;
const privateFile = /\.(db(?:-|$)|sqlite(?:-|$)|sqlite3(?:-|$)|log$|eml$)/i;

for (const file of tracked) {
  if (privatePath.test(file)) failures.push(`${file}: private runtime path must not be tracked`);
  const base = file.split('/').pop() || '';
  if ((base === '.env' || base.startsWith('.env.')) && !allowedEnvFiles.has(base)) {
    failures.push(`${file}: environment file must not be tracked`);
  }
  if (privateFile.test(file)) failures.push(`${file}: private/runtime artifact type must not be tracked`);
}
const ignoreLines = new Set(readFileSync(resolve(root, '.gitignore'), 'utf8')
  .split(/\r?\n/).map((line) => line.trim()).filter(Boolean));
const requiredIgnores = [
  'data/', 'runtime/', '.runtime/', 'private-runtime/',
  '*.db', '*.sqlite', '*.sqlite3', '*.log', '*.eml',
  '.env', '.env.*', 'build-g1/',
];
for (const pattern of requiredIgnores) {
  if (!ignoreLines.has(pattern)) failures.push(`.gitignore: missing ${pattern}`);
}

const sidecar = readFileSync(resolve(root, 'tools/mail-ai-agent/server.mjs'), 'utf8');
const sidecarGuards = [
  ['raw exception logging', /console\.error\s*\(\s*error\s*\)/],
  ['raw exception response', /res\.end\([^\n;]*error\.(message|stack)/],
  ['request body logging', /console\.(log|error|warn)\s*\(\s*(body|messages|context|prompt|result|approval)\b/],
];
for (const [label, pattern] of sidecarGuards) {
  if (pattern.test(sidecar)) failures.push(`tools/mail-ai-agent/server.mjs: ${label}`);
}
const bridge = readFileSync(resolve(root, 'backend/web/api_mail_ai.go'), 'utf8');
if (/logHandlerError\([^\n]*%w/.test(bridge)) {
  failures.push('backend/web/api_mail_ai.go: raw wrapped error may reach regular logs');
}
if (/log\.Printf\s*\(/.test(bridge)) {
  failures.push('backend/web/api_mail_ai.go: direct log.Printf is not allowed on the Mail AI bridge');
}

if (failures.length > 0) {
  console.error(`Private data boundary check failed (${failures.length} rule(s)):`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log(JSON.stringify({
  ok: true,
  tracked_files_checked: tracked.length,
  private_artifacts_tracked: 0,
  unsafe_mail_ai_log_patterns: 0,
}));
