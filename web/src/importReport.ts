// 导入报告的纯逻辑：条目排序、页号展示、备注覆盖率、错误归类、逐页标记释义。
//
// 这些函数独立于组件，是因为它们各自都出过（或极易出）"看起来对、实际上错一格"的问题：
// 0 基页序直接展示会出现"第 0 页"；pageCount=0 时算覆盖率会得到 NaN；
// 后端新增条目代码时若不排序，用户会先看到"有 3 页隐藏"再看到"有 1 页内容会丢"。

export type ReportSeverity = 'warn' | 'info';

export type ReportFinding = {
  code: string;
  severity: ReportSeverity;
  count: number;
  pages?: number[];
};

// 已知条目代码的展示顺序：与后端 BuildImportReport 的追加顺序一致。
// 未知代码（后端先上线、前端还没跟）排在已知之后，仍要显示——宁可露出代码，也不能悄悄吞掉一条风险。
const KNOWN_FINDINGS = [
  'opaque_shapes',
  'embedded_media',
  'pages_without_text',
  'parse_diagnostics',
  'animations',
  'hidden_pages',
  'missing_notes'
] as const;

// PAGE_LIST_LIMIT 是单条结论最多列出的页数：列 100 页等于没列，
// 但"还有几页没显示"必须跟着说，否则用户以为风险只涉及这几页。
export const PAGE_LIST_LIMIT = 8;

export function severityRank(severity: string): number {
  return severity === 'warn' ? 0 : 1;
}

export function orderFindings<T extends { severity: string; code: string; count: number }>(findings: T[]): T[] {
  const known = new Map<string, number>(KNOWN_FINDINGS.map((code, index) => [code, index]));
  return [...findings].sort((a, b) => {
    const bySeverity = severityRank(a.severity) - severityRank(b.severity);
    if (bySeverity !== 0) return bySeverity;
    const ka = known.get(a.code) ?? KNOWN_FINDINGS.length;
    const kb = known.get(b.code) ?? KNOWN_FINDINGS.length;
    if (ka !== kb) return ka - kb;
    return b.count - a.count;
  });
}

// humanizePages 把 0 基页序转成 1 基的展示文本。
// 空数组返回空串；超过 limit 页只列前 limit 页，并由 truncated 告诉调用方加"等 N 页"。
export function humanizePages(pages: number[] | undefined, limit = PAGE_LIST_LIMIT): { text: string; truncated: boolean } {
  if (!pages || pages.length === 0) return { text: '', truncated: false };
  const unique = Array.from(new Set(pages.filter((p) => Number.isFinite(p) && p >= 0))).sort((a, b) => a - b);
  if (unique.length === 0) return { text: '', truncated: false };
  const shown = unique.slice(0, Math.max(1, limit));
  return {
    text: shown.map((p) => String(p + 1)).join('、'),
    truncated: unique.length > shown.length
  };
}

// notesCoverage 返回"有源文件备注的页"占比（整数百分比）。
// 0 页时返回 0 而不是 NaN —— NaN 会被模板拼成 "NaN%" 直接糊到界面上。
export function notesCoverage(pageCount: number, pagesWithNotes: number): number {
  if (!Number.isFinite(pageCount) || pageCount <= 0) return 0;
  const withNotes = Math.min(Math.max(pagesWithNotes, 0), pageCount);
  return Math.round((withNotes / pageCount) * 100);
}

// pageCountMismatch 判断"解析回写页数"与"解析产物页数"是否对不上。
// 两者都 >0 才比较：0 表示没写回/没解析，不是"不一致"。
export function pageCountMismatch(reportPageCount: number, revisionPageCount: number): boolean {
  if (!Number.isFinite(reportPageCount) || !Number.isFinite(revisionPageCount)) return false;
  if (reportPageCount <= 0 || revisionPageCount <= 0) return false;
  return reportPageCount !== revisionPageCount;
}

export type ReportFailure = 'not_ready' | 'revision_missing' | 'corrupt' | 'invalid' | 'forbidden' | 'unknown';

// classifyReportError 把服务端 code 折成用户下一步该做什么：
// 等待解析 / 版本被删 / 产物损坏需联系维护 / 参数错 / 无权限。
export function classifyReportError(code: string | undefined): ReportFailure {
  switch (code) {
    case 'report_not_ready':
      return 'not_ready';
    case 'revision_not_found':
      return 'revision_missing';
    case 'report_corrupt':
      return 'corrupt';
    case 'invalid_revision':
      return 'invalid';
    case 'http_401':
    case 'http_403':
      return 'forbidden';
    default:
      return 'unknown';
  }
}

export type FlagKind =
  | 'hidden'
  | 'timing'
  | 'transition'
  | 'auto_advance'
  | 'picture'
  | 'audio'
  | 'video'
  | 'chart'
  | 'table'
  | 'userNotes'
  | 'opaqueShape'
  | 'unknown';

// describeFlag 把后端 featureFlags 释义成"人话 + 可能的细节"。
// 未识别标记不丢弃：原样展示，用户才能把界面上的怪现象和文件里的事实对上。
export function describeFlag(flag: string): { kind: FlagKind; detail: string } {
  if (flag.startsWith('opaque_shape:')) {
    return { kind: 'opaqueShape', detail: flag.slice('opaque_shape:'.length) };
  }
  switch (flag) {
    case 'hidden':
    case 'timing':
    case 'transition':
    case 'auto_advance':
    case 'picture':
    case 'audio':
    case 'video':
    case 'chart':
    case 'table':
    case 'userNotes':
      return { kind: flag, detail: '' };
    default:
      return { kind: 'unknown', detail: flag };
  }
}
