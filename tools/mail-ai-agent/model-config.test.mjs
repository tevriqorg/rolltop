import assert from 'node:assert/strict';
import test from 'node:test';
import { nextModelConfig, normalizeModelConfigValues } from './model-config.mjs';

test('model config normalization rejects unsafe endpoint URLs', () => {
  assert.deepEqual(normalizeModelConfigValues('https://models.example/v1/', 'gpt-x'), {
    baseURL: 'https://models.example/v1', model: 'gpt-x',
  });
  assert.throws(() => normalizeModelConfigValues('https://user:pass@models.example/v1', 'gpt-x'), /without credentials/);
  assert.throws(() => normalizeModelConfigValues('https://models.example/v1?token=x', 'gpt-x'), /without credentials/);
});

test('changing endpoint cannot reuse an existing saved key implicitly', () => {
  const current = { baseURL: 'https://old.example/v1', model: 'old', apiKey: 'saved-secret' };
  assert.throws(
    () => nextModelConfig(current, { base_url: 'https://new.example/v1', model: 'new' }),
    /requires a new api_key or clear_api_key/,
  );
  assert.deepEqual(nextModelConfig(current, {
    base_url: 'https://new.example/v1', model: 'new', api_key: 'new-secret',
  }), { baseURL: 'https://new.example/v1', model: 'new', apiKey: 'new-secret' });
  assert.deepEqual(nextModelConfig(current, {
    base_url: 'https://new.example/v1', model: 'new', clear_api_key: true,
  }), { baseURL: 'https://new.example/v1', model: 'new', apiKey: '' });
});

test('same endpoint can retain the existing saved key', () => {
  const current = { baseURL: 'https://same.example/v1', model: 'old', apiKey: 'saved-secret' };
  assert.deepEqual(nextModelConfig(current, {
    base_url: 'https://same.example/v1', model: 'new',
  }), { baseURL: 'https://same.example/v1', model: 'new', apiKey: 'saved-secret' });
});
