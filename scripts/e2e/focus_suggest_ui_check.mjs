// 焦点建议的**点击级**验收：真浏览器里挂载真实的 ScriptEditor，看用户点下去以后
// 界面显示了什么、保存通道收到了什么。
//
// 为什么需要这一层：script-focus-check.mjs 是纯函数 + 源码接线断言，go handler 测试
// 验的是服务端契约。两者都证明不了「按钮真的能点、菜单里真的只出现可选目标、
// 点完真的写进讲稿段落」。尤其是 `"focus": null` 这种错误，接口会 200、存稿会成功，
// 只有把真正发出去的 segments 拿来逐键检查才看得见。
//
// 夹具由 focus-suggest-ui.sh 临时生成到 web/ 并用 vite dev 提供；这里不 mock 组件，
// 只 mock 网络边界（commit / 建议请求），产品代码全是真的。
import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import { connectCDP, goto, waitFor, clickButtonExpr, buttonStateExpr } from './cdp.mjs';

const BASE = process.env.BASE || 'http://127.0.0.1:5179';
const CDP_PORT = Number(process.env.CDP_PORT || 9337);
const SHOT = process.env.SHOT || '';

const cdp = await connectCDP(CDP_PORT, 'focus-suggest-harness.html');
const ev = async (expr) => {
  try {
    return await cdp.evaluate(expr);
  } catch {
    // 页面导航的瞬间 Runtime.evaluate 会撞上 context destroyed；交给 waitFor 重试。
    return null;
  }
};
const until = (expr, label) =>
  waitFor(async () => await ev(expr), { label, timeoutMs: 15000, intervalMs: 120 });
const click = (text) => ev(clickButtonExpr(text));
const focusTrigger = (index) => ev(`(() => {
  const b = document.querySelectorAll('.segment-focus-trigger')[${index}];
  if (!b) return 'notfound';
  b.click();
  return 'ok';
})()`);
const bodyText = () => ev('document.body.innerText');

async function navigate(query = '') {
  await goto(cdp, `${BASE}/focus-suggest-harness.html${query}`);
  await until(
    `document.querySelectorAll('.segment-card').length === 4 && !!window.__H`,
    '真实 ScriptEditor 挂载完成'
  );
}

let checks = 0;
let failures = 0;
const check = (label, fn) => {
  checks++;
  try {
    fn();
    console.log('  ✓ ' + label);
  } catch (e) {
    failures++;
    console.log('  ✗ ' + label + '：' + String(e.message || e).split('\n')[0]);
  }
};

try {
  console.log('== A 结构化页：按需拉取 / 候选闸门 / 采纳走保存通道');
  await navigate();
  const requests0 = await ev('window.__H.requests');
  check('页面加载不发建议请求（不是自动预填）', () => assert.equal(requests0, 0));
  const acceptIdle = await ev(buttonStateExpr('采纳唯一候选（0）'));
  check('没取建议时「采纳唯一候选（0）」禁用', () => assert.equal(acceptIdle, 'disabled'));

  const clickedRefresh = await click('用规则找建议');
  check('「用规则找建议」可点击', () => assert.equal(clickedRefresh, 'ok'));
  await until(
    `window.__H.requests === 1 && document.body.innerText.includes('找到 4 段建议')`,
    '建议条按真实条数给出回执'
  );
  const acceptReady = await ev(buttonStateExpr('采纳唯一候选（1）'));
  check('可采纳数 = 1（多候选 / 已人工指过 / 不可选目标都不算）', () =>
    assert.equal(acceptReady, 'enabled')
  );

  assert.equal(await focusTrigger(0), 'ok');
  await until(`document.querySelectorAll('.segment-focus-suggestions').length === 1`, '第 1 段建议分区出现');
  const firstMenu = await ev(`(() => {
    const g = document.querySelector('.segment-focus-suggestions');
    return { buttons: g.querySelectorAll('button').length, text: g.innerText };
  })()`);
  check('唯一候选段只列 1 个建议，且用候选清单的可读标签', () => {
    assert.equal(firstMenu.buttons, 1);
    assert.match(firstMenu.text, /标题：本季度回顾/);
    assert.match(firstMenu.text, /建议/);
    assert.doesNotMatch(firstMenu.text, /ghost|chart-1/, '裸 shapeId 不能出现在用户面前');
  });

  assert.equal(await focusTrigger(1), 'ok');
  await until(
    `document.querySelectorAll('.segment-focus-suggestions button').length === 2`,
    '第 2 段两个候选都列出来让人挑'
  );
  const multiMenu = await ev(`document.querySelector('.segment-focus-suggestions').innerText`);
  check('多候选段两个都列，不替用户挑一个', () => {
    assert.match(multiMenu, /标题：本季度回顾/);
    assert.match(multiMenu, /第一条：华东 174 秒/);
  });

  assert.equal(await focusTrigger(3), 'ok');
  await until(
    `document.querySelectorAll('.segment-focus-trigger')[3].getAttribute('aria-expanded') === 'true'`,
    '第 4 段菜单展开'
  );
  const ghostShown = await ev(`document.querySelectorAll('.segment-focus-suggestions').length`);
  check('候选清单外的目标不出现在菜单里', () => assert.equal(ghostShown, 0));
  await focusTrigger(3); // 收起，避免浮层挡住后面的按钮

  const before = await ev('window.__H.commits.length');
  assert.equal(await click('采纳唯一候选（1）'), 'ok');
  await until(`window.__H.commits.length > ${before}`, '批量采纳真的走到了保存通道');
  const commit1 = await ev('window.__H.commits.at(-1)');
  check('采纳后发出的段落：只给唯一候选段写 focus', () => {
    assert.deepEqual(commit1[0].focus, { shapeId: 's1', barId: '' });
    assert.equal('focus' in commit1[1], false, '多候选段必须「不表态」');
    assert.deepEqual(commit1[2].focus, { shapeId: 's1' }, '人工已指的段原样保留');
    assert.equal('focus' in commit1[3], false, '不可选目标段不能被写入');
    assert.doesNotMatch(JSON.stringify(commit1), /"focus":null/, '不能出现 focus:null（服务端按清空处理）');
  });
  await until(
    `document.body.innerText.includes('已把 1 个唯一候选填入本页焦点')`,
    '采纳回执如实显示个数'
  );
  const acceptNotice = await bodyText();
  check('回执说明正在自动保存，不假装已经落库完成', () =>
    assert.match(acceptNotice, /正在自动保存/)
  );

  const before2 = await ev('window.__H.commits.length');
  assert.equal(await focusTrigger(1), 'ok');
  await until(`document.querySelectorAll('.segment-focus-suggestions button').length === 2`, '第 2 段建议再次展开');
  await ev(`document.querySelectorAll('.segment-focus-suggestions button')[1].click()`);
  await until(`window.__H.commits.length > ${before2}`, '逐段点选建议也走同一条保存通道');
  const commit2 = await ev('window.__H.commits.at(-1)');
  check('逐段点选写的是被点的那个目标，其余段仍保持三态', () => {
    assert.equal(commit2[1].focus.shapeId, 's2');
    assert.equal('focus' in commit2[3], false);
    assert.doesNotMatch(JSON.stringify(commit2), /"focus":null/);
  });

  console.log('== B 低置信页：不自动建议，界面给出可行动的说明');
  await navigate('?band=low-confidence');
  const reqB0 = await ev('window.__H.requests');
  check('低置信页同样不自动请求', () => assert.equal(reqB0, 0));
  assert.equal(await click('用规则找建议'), 'ok');
  await until(
    `window.__H.requests === 1 && document.body.innerText.includes('这一页缺少结构信号')`,
    '低置信页给出本地化说明'
  );
  const acceptB = await ev(buttonStateExpr('采纳唯一候选（0）'));
  check('没有建议时采纳按钮禁用', () => assert.equal(acceptB, 'disabled'));
  const lowText = await bodyText();
  check('低置信页不谎报「找到 N 段建议」，也不贴服务端原始说明', () => {
    assert.doesNotMatch(lowText, /找到 \d+ 段建议/);
    assert.doesNotMatch(lowText, /0\.53|0\.67|BandReason/, '服务端 note 的中文/数值不能混进界面文案');
  });

  if (SHOT) {
    const { data } = await cdp.send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(SHOT, Buffer.from(data, 'base64'));
    console.log('  截图：' + SHOT);
  }
} catch (e) {
  failures++;
  console.log('  ✗ 流程中断：' + (e?.message || e));
} finally {
  cdp.close();
}

if (failures > 0) {
  console.error(`\nFAIL：焦点建议点击级验收未通过（${failures}/${checks} 项）`);
  process.exit(1);
}
console.log(`\nPASS：焦点建议在真浏览器里按需拉取、只列可选目标、采纳与点选都走既有保存通道（${checks} 项）`);
