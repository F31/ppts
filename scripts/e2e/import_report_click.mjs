// 导入报告页的界面核验：真实浏览器打开真服务上的真数据，检查"页面上长出来的东西"。
//
// 纯逻辑（页序 1 基、排序、覆盖率）已由 web/import-report-check.mjs 钉住；这里只看
// 单测看不到的：接口字段有没有真的渲染上去、隐藏页有没有被标出来、页表行数对不对。
import { writeFile } from 'node:fs/promises';
import { connectCDP, goto, waitFor, bodyTextExpr } from './cdp.mjs';

// 隐藏页必须在标记列里明确写出来：整行压暗只是视觉暗示，不足以解释"这页为什么不在成片里"。
function documentHasHiddenChip(snapshot) {
  return Number(snapshot.hiddenFlagChips) > 0;
}

function arg(name, fallback = '') {
  const hit = process.argv.find((a) => a.startsWith(`--${name}=`));
  return hit ? hit.slice(name.length + 3) : fallback;
}

const port = arg('port', '38084');
const cdpPort = Number(arg('cdp', '9224'));
const project = arg('project');
const rev = arg('rev', '1');
const expectHidden = Number(arg('hidden', '0'));
const shot = arg('shot', '');

const cdp = await connectCDP(cdpPort);
try {
  const url = `http://127.0.0.1:${port}/projects/${project}/report?rev=${rev}`;
  await goto(cdp, url);

  await waitFor(
    async () => {
      const text = await cdp.evaluate(bodyTextExpr);
      return typeof text === 'string' && text.includes('逐页') ? text : '';
    },
    { timeoutMs: 20000, label: '报告页渲染' }
  );

  const snapshot = await cdp.evaluate(`(() => {
    const text = (sel) => document.querySelector(sel)?.textContent?.trim() || '';
    const rows = [...document.querySelectorAll('.report-pages tbody tr')];
    const stats = [...document.querySelectorAll('.stat-card')].map((c) => ({
      label: c.querySelector('.stat-label')?.textContent?.trim() || '',
      value: c.querySelector('.stat-value')?.textContent?.trim() || ''
    }));
    return {
      // 顶栏也有 .eyebrow（品牌名），必须限定在页面头部里取。
      eyebrow: text('.page-header-row .eyebrow'),
      h1: text('h1'),
      sub: text('.page-sub'),
      stats,
      findings: [...document.querySelectorAll('.report-finding')].map((f) => ({
        severity: f.className.replace('report-finding ', ''),
        text: f.querySelector('.report-finding-text')?.textContent?.trim() || ''
      })),
      rowCount: rows.length,
      firstRow: rows[0] ? [...rows[0].children].map((td) => td.textContent.trim()) : [],
      hiddenRows: document.querySelectorAll('tr.report-page-hidden').length,
      hiddenFlagChips: [...document.querySelectorAll('tr.report-page-hidden .report-flag')]
        .filter((el) => (el.textContent || '').trim() === '隐藏页').length,
      bodyHasNaN: document.body.innerText.includes('NaN'),
      bodyHasUndefined: document.body.innerText.includes('undefined')
    };
  })()`);

  const problems = [];
  if (snapshot.eyebrow !== '导入报告') problems.push(`eyebrow = ${snapshot.eyebrow}`);
  if (!snapshot.h1) problems.push('缺少项目标题');
  if (!/v1/.test(snapshot.sub) || !/go-pptx/.test(snapshot.sub)) problems.push(`副标题 = ${snapshot.sub}`);
  if (snapshot.stats.length !== 4) problems.push(`统计卡数量 = ${snapshot.stats.length}`);
  const pagesStat = snapshot.stats.find((s) => s.label === '页数');
  if (!pagesStat || !(Number(pagesStat.value) > 0)) problems.push(`页数卡 = ${JSON.stringify(pagesStat)}`);
  if (snapshot.rowCount !== Number(pagesStat?.value ?? -1)) {
    problems.push(`页表 ${snapshot.rowCount} 行 != 页数 ${pagesStat?.value}`);
  }
  // 0 基页序直接上屏会显示 0，这里第一行必须是 1。
  if (snapshot.firstRow[0] !== '1') problems.push(`首行页序 = ${snapshot.firstRow[0]}（应显示 1）`);
  if (snapshot.bodyHasNaN) problems.push('页面出现 NaN');
  if (snapshot.bodyHasUndefined) problems.push('页面出现 undefined');
  if (snapshot.findings.length === 0) problems.push('没有任何结论条目');
  if (snapshot.hiddenRows !== expectHidden) problems.push(`隐藏行 ${snapshot.hiddenRows} != 期望 ${expectHidden}`);
  if (expectHidden > 0 && !documentHasHiddenChip(snapshot)) problems.push('隐藏页没有「隐藏页」标记');

  if (shot) {
    const res = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true });
    await writeFile(shot, Buffer.from(res.data, 'base64'));
  }

  console.log(JSON.stringify(snapshot, null, 2));
  if (problems.length) {
    console.error('\n界面核验未通过：');
    for (const p of problems) console.error('  -', p);
    process.exit(1);
  }
  console.log('\n界面核验通过');
} finally {
  cdp.close();
}
