#!/usr/bin/env node
/**
 * local-auth-check —— 本地单租户实例不能把用户锁在门外。
 *
 * 真实事故：在本地实例点「退出登录」→ 清空身份 → 跳 /login。而本地实例
 * email_password=false、没有 OIDC、生产构建又关闭开发身份表单，登录页只剩
 * 「登录服务尚未配置，请联系管理员」。用户没有任何办法回到应用，
 * 因为本地模式根本没有账号可以登录。
 *
 * 这里守四件事：
 *   - 身份判定：local=true 但缺 tenant/user 不算本地身份（造出来的会话服务端也不认）；
 *   - 登出语义：本地模式不存在"登出"，只能保持会话；
 *   - 身份丢失后自动恢复（登出、401 清理都不会停在登录页）；
 *   - 界面接线：菜单不渲染退出按钮、登录页给出「进入本机实例」。
 */
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const m = await import('./src/localAuth.ts');
const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');

test('local=true 且带齐 tenant/user 才是本地身份', () => {
  const id = m.localIdentityFromConfig({
    local: true,
    tenant_id: '00000000-0000-0000-0000-000000000001',
    user_id: 'local-user',
    tenant_type: 'personal',
    tenant_name: 'Local'
  });
  assert.deepEqual(id, {
    tenantId: '00000000-0000-0000-0000-000000000001',
    userId: 'local-user',
    tenantType: 'personal',
    tenantName: 'Local'
  });
});

test('缺 tenant/user 不算本地身份——不能凭 local:true 造一个空会话', () => {
  assert.equal(m.localIdentityFromConfig({ local: true, user_id: 'local-user' }), null);
  assert.equal(m.localIdentityFromConfig({ local: true, tenant_id: 't1' }), null);
  assert.equal(m.localIdentityFromConfig({ local: true, tenant_id: '  ', user_id: 'u' }), null);
  assert.equal(m.localIdentityFromConfig({ local: true }), null);
});

test('多租户配置（无 local）一律不是本地身份', () => {
  assert.equal(m.localIdentityFromConfig({ email_password: true }), null);
  assert.equal(m.localIdentityFromConfig({ local: false, tenant_id: 't', user_id: 'u' }), null);
  assert.equal(m.localIdentityFromConfig(null), null);
  assert.equal(m.localIdentityFromConfig(undefined), null);
});

test('身份字段两端空白被裁掉', () => {
  assert.deepEqual(m.localIdentityFromConfig({ local: true, tenant_id: ' t1 ', user_id: ' u1 ' }), {
    tenantId: 't1',
    userId: 'u1',
    tenantType: undefined,
    tenantName: undefined
  });
});

test('本地模式没有登出：必须保持会话，不能走 sign-out', () => {
  const local = { tenantId: 't1', userId: 'local-user' };
  assert.equal(m.logoutOutcome(local), 'local-reenter');
  assert.equal(m.logoutOutcome(null), 'sign-out');
});

test('登录页只在拿到完整本地身份时才给入口', () => {
  assert.equal(m.canEnterLocalFromLogin({ tenantId: 't1', userId: 'local-user' }), true);
  assert.equal(m.canEnterLocalFromLogin(null), false);
});

test('App：登出前先判本地模式，本地模式不得清空身份', () => {
  const src = read('./src/App.tsx');
  const logoutBody = src.slice(src.indexOf('const logout = useCallback'), src.indexOf('const session: Session'));
  assert.ok(logoutBody.includes('logoutOutcome(localIdentity)'), '登出必须经过 logoutOutcome 判定');
  // 判定必须早于 clearAllIdentity —— 否则顺序一颠倒，本地模式照样被清空。
  assert.ok(
    logoutBody.indexOf('logoutOutcome(localIdentity)') < logoutBody.indexOf('clearAllIdentity()'),
    '本地模式判定必须在清除身份之前'
  );
});

test('App：身份丢失后自动恢复本地会话', () => {
  const src = read('./src/App.tsx');
  assert.match(src, /if \(!identity && localIdentity\)/, '缺少本地身份自动恢复');
  assert.match(src, /if \(localRef\.current\)/, '401 处理器必须识别本地模式');
  assert.match(src, /localIdentityFromConfig\(cfg\)/, '认证探测必须复用同一份本地身份判定');
});

test('菜单：本地模式不渲染「退出登录」', () => {
  const src = read('./src/AppShell.tsx');
  assert.match(src, /local \? \(\s*<p className="user-menu-note">\{t\('user\.localSession'\)\}<\/p>\s*\) : \(\s*<button[^>]*danger/, '本地模式应显示说明而非退出按钮');
  assert.ok(src.includes("t('user.logout')"), '多租户模式仍要保留退出入口');
});

test('登录页：本地模式给出「进入本机实例」，不再只显示未配置', () => {
  const src = read('./src/pages/Login.tsx');
  assert.match(src, /canEnterLocalFromLogin\(localEntry\)/, '登录页缺少本地入口判定');
  assert.match(src, /t\('login\.enterLocal'\)/, '缺少本机实例入口文案');
  assert.match(src, /loginLocal\(localEntry\)/, '入口必须用服务端下发的固定身份');
});
