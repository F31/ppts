// 讲稿并排对照（G2-2）：把「原文素材」与「当前讲稿」切成句子对齐，供用户逐句接受/回退。
//
// 为什么在前端做而不是后端返回 diff：
//   - 对齐阈值与展示顺序是**交互参数**（用户点「改用原文」要立即生效），来回请求会让
//     每一次点击都变慢，还会把"尚未保存的本地草稿"拿去和服务端 diff；
//   - 但「原文是哪一段」必须由服务端给（见 internal/api/scriptcompare.go 的注释），
//     前端不自己拼形状文字。
//
// 纯函数、无 DOM 依赖：script-compare-check.mjs 直接加载本文件做门禁。

export type CompareRowKind = 'same' | 'changed' | 'added' | 'removed';

// CompareRow 是一行对照：原文一句 / 讲稿一句 / 两者都有。
//   - same    : 两句实质相同（仅标点/空白差异），无需选择
//   - changed : 对齐成功但措辞不同 → 可选「润色」或「原文」
//   - added   : 只在讲稿里出现（模型新增）→ 可选择删掉
//   - removed : 只在原文里出现（模型删掉）→ 可选择补回
export type CompareRow = {
  key: string;
  kind: CompareRowKind;
  original?: string;
  current?: string;
  similarity?: number;
};

// CompareChoice 表示这一行最终采用哪一侧；same 行两侧等价，仍存 'current' 便于统一处理。
export type CompareChoice = 'current' | 'original';

// MATCH_THRESHOLD 是对齐门槛（字符二元组 Dice 系数）。
//
// 定在 0.4 的依据来自"润色"的实际形态：保留事实、换口语表达时，同义句的字符二元组
// 重合通常仍在 0.5 以上；而完全改写的句子会落到 0.2 以下。门槛过高会把改写拆成
// "删除+新增"两行（用户要多点一次），过低会把无关句硬配对（用户会误以为模型改错了）。
export const MATCH_THRESHOLD = 0.4;

// SAME_THRESHOLD 以上视为"没改"：只剩标点/空白差异，不值得让用户做选择。
export const SAME_THRESHOLD = 0.95;

const CJK_TERMINATORS = '。！？；…';
// 句末引号/括号要跟着上一句走，否则会出现 “你好。” 被拆成 「“你好。」+「”」两行。
const CLOSERS = '”’」』）)》〉"\'';
// 这些字符结尾时，下一句直接接上、不加空格（中文排版）。
const NO_SPACE_AFTER = '。！？；…：、，）」』”’》〉"\'';
// 英文常见缩写：切句时不能把 "e.g." / "Mr." 当成句末。
const ABBREVIATIONS = new Set(['mr', 'mrs', 'ms', 'dr', 'prof', 'vs', 'etc', 'e.g', 'i.e', 'no', 'fig', 'inc', 'ltd', 'co']);

// splitSentences 把一段讲稿切成句子。返回的句子保留原标点，便于原样拼回。
//
// 切分规则刻意保守：
//   - 中文按 。！？；… 与换行切；
//   - 英文句点只在「后面跟空白且不是小数/缩写」时切（23.5% / e.g. 不会被切开）；
//   - 空白句（纯格式残留）直接丢弃，避免对照面板出现空行。
//
// **换行随句子保留**（句尾带 '\n'）：多数讲稿的页内结构就是"标题一行、要点一行"，
// 若切句时把换行丢掉，"不改任何选择直接采用"会把多行讲稿压成一段（见 check 里的不变量）。
export function splitSentences(text: string): string[] {
  const src = (text ?? '').replace(/\r\n?/g, '\n');
  const out: string[] = [];
  let buf = '';
  const flush = (keepBreak = false) => {
    const trimmed = buf.trim();
    if (trimmed) out.push(keepBreak ? `${trimmed}\n` : trimmed);
    buf = '';
  };
  for (let i = 0; i < src.length; i += 1) {
    const ch = src[i];
    if (ch === '\n') {
      if (buf.trim()) {
        flush(true);
      } else if (out.length > 0 && !out[out.length - 1].endsWith('\n\n')) {
        // 空行也保留（最多一个），避免整页排版被压平。
        out[out.length - 1] += '\n';
      }
      buf = '';
      continue;
    }
    buf += ch;
    if (CJK_TERMINATORS.includes(ch)) {
      // 吸收重复终止符与句末引号：「真的吗？！」「他说：“好。”」
      while (i + 1 < src.length && (CJK_TERMINATORS.includes(src[i + 1]) || CLOSERS.includes(src[i + 1]))) {
        buf += src[i + 1];
        i += 1;
      }
      flush();
      continue;
    }
    if (ch === '.' && shouldSplitAtDot(buf, src, i)) {
      // 把句点后的空白一并吃掉，避免成为下一句的前导空格
      let j = i + 1;
      while (j < src.length && /\s/.test(src[j]) && src[j] !== '\n') j += 1;
      buf += src.slice(i + 1, j);
      i = j - 1;
      flush();
    }
  }
  flush();
  return out;
}

function shouldSplitAtDot(buf: string, src: string, index: number): boolean {
  const next = src[index + 1];
  if (next === undefined) return false;
  if (!/\s/.test(next)) return false; // 23.5% / 3.14 / a.b 不切
  const prev = buf[buf.length - 2] ?? '';
  if (/\d/.test(prev)) return false; // 12. 之后是空格也当作序号/小数，不切
  const word = (buf.match(/([A-Za-z.]+)\.$/)?.[1] ?? '').toLowerCase();
  if (word && ABBREVIATIONS.has(word)) return false;
  return true;
}

// joinSentences 把句子拼回一段文本：中文句末不加空格，英文句末补一个空格。
// 句子自带的换行（splitSentences 的页内结构）原样保留，不会被空格顶掉。
export function joinSentences(parts: string[]): string {
  let out = '';
  for (const part of parts) {
    // 只清掉行首/行尾的空格与制表符：'\n' 是结构信息，trim() 会把它一起吃掉。
    const text = part.replace(/^[ \t]+|[ \t]+$/g, '');
    if (!text.trim()) continue;
    if (!out) {
      out = text;
      continue;
    }
    const last = out[out.length - 1];
    out += NO_SPACE_AFTER.includes(last) || last === '\n' ? text : ` ${text}`;
  }
  return out;
}

// displaySentence 是对照面板的展示文本：去掉句尾换行，避免 pre-wrap 渲染出额外空行。
export function displaySentence(text?: string): string {
  return (text ?? '').replace(/\n+$/, '');
}

// normalizeForCompare 去掉标点/空白后比较：润色稿常见的改动就是把「，」改成「。」，
// 若把标点计入相似度，"只加了逗号"的句子会被判成改写。
function normalizeForCompare(text: string): string {
  return text.replace(/[\s\p{P}\p{S}]/gu, '').toLowerCase();
}

function bigrams(text: string): Set<string> {
  const set = new Set<string>();
  for (let i = 0; i < text.length - 1; i += 1) set.add(text.slice(i, i + 2));
  return set;
}

// sentenceSimilarity 用字符二元组 Dice 系数衡量两句相似度（0..1）。
// 中文没有分词边界，二元组比单词重合更稳；英文混排时也同样可用。
export function sentenceSimilarity(a: string, b: string): number {
  const left = normalizeForCompare(a);
  const right = normalizeForCompare(b);
  if (!left || !right) return 0;
  if (left === right) return 1;
  if (left.length < 2 || right.length < 2) return left === right ? 1 : 0;
  const A = bigrams(left);
  const B = bigrams(right);
  let overlap = 0;
  for (const gram of A) {
    if (B.has(gram)) overlap += 1;
  }
  return (2 * overlap) / (A.size + B.size);
}

type RawRow = Omit<CompareRow, 'key'>;

// alignSentences 用加权 LCS 对齐两句集合：
//   score[i][j] = max(score[i+1][j], score[i][j+1], sim(i,j) + score[i+1][j+1])
// 只有相似度达到 MATCH_THRESHOLD 才允许配对，避免"为了对齐而对齐"。
function alignSentences(original: string[], current: string[]): RawRow[] {
  const n = original.length;
  const m = current.length;
  if (n === 0) return current.map((text) => ({ kind: 'added' as const, current: text }));
  if (m === 0) return original.map((text) => ({ kind: 'removed' as const, original: text }));

  const sim: number[][] = Array.from({ length: n }, () => new Array<number>(m).fill(0));
  const score: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      const s = sentenceSimilarity(original[i], current[j]);
      sim[i][j] = s;
      let best = Math.max(score[i + 1][j], score[i][j + 1]);
      if (s >= MATCH_THRESHOLD) best = Math.max(best, s + score[i + 1][j + 1]);
      score[i][j] = best;
    }
  }

  const rows: RawRow[] = [];
  let i = 0;
  let j = 0;
  const EPS = 1e-9;
  while (i < n && j < m) {
    const s = sim[i][j];
    if (s >= MATCH_THRESHOLD && Math.abs(score[i][j] - (s + score[i + 1][j + 1])) < EPS) {
      rows.push({
        kind: s >= SAME_THRESHOLD ? 'same' : 'changed',
        original: original[i],
        current: current[j],
        similarity: s
      });
      i += 1;
      j += 1;
      continue;
    }
    // 平局时先消费原文：删除行会落在它原本的位置上，补回时顺序不会跑到段末。
    if (score[i + 1][j] >= score[i][j + 1]) {
      rows.push({ kind: 'removed', original: original[i] });
      i += 1;
    } else {
      rows.push({ kind: 'added', current: current[j] });
      j += 1;
    }
  }
  while (i < n) {
    rows.push({ kind: 'removed', original: original[i] });
    i += 1;
  }
  while (j < m) {
    rows.push({ kind: 'added', current: current[j] });
    j += 1;
  }
  return rows;
}

// buildCompareRows 生成带稳定 key 的对照行。key 由序号+类型构成：同一份数据重复构建
// 得到同一组 key，选择状态不会在 React 重渲染时错位。
export function buildCompareRows(originalText: string, currentText: string): CompareRow[] {
  const rows = alignSentences(splitSentences(originalText), splitSentences(currentText));
  return rows.map((row, index) => ({ ...row, key: `${index}:${row.kind}` }));
}

// defaultCompareChoice 的默认值是「接受建议稿」：改写取润色、新增保留、删除不补回。
// 这样"不改任何选择直接采用"得到的文本就是当前讲稿本身（见 check 里的不变量断言）。
export function defaultCompareChoice(_row: CompareRow): CompareChoice {
  // removed 行同样取 'current'：表示"这句保持删除状态"，该行不贡献任何文本。
  return 'current';
}

// mergeCompareRows 按选择拼出最终文本。
// 顺序由对照行顺序决定，因此「补回原文」会回到它原来的位置，而不是被追加到末尾。
export function mergeCompareRows(rows: CompareRow[], choices: Record<string, CompareChoice>): string {
  const parts: string[] = [];
  for (const row of rows) {
    const choice = choices[row.key] ?? defaultCompareChoice(row);
    const text = choice === 'original' ? row.original : row.current;
    if (text && text.trim()) parts.push(text);
  }
  return joinSentences(parts);
}

export type CompareSummary = {
  total: number;
  same: number;
  changed: number;
  added: number;
  removed: number;
  reverted: number; // 用户把某行改回原文（含补回删除句）
  dropped: number; // 用户把某行排除（删除新增句 / 仍删除原句）
};

// summarizeCompare 给面板底部提供"这次会改什么"的确定数字。
export function summarizeCompare(rows: CompareRow[], choices: Record<string, CompareChoice>): CompareSummary {
  const summary: CompareSummary = { total: rows.length, same: 0, changed: 0, added: 0, removed: 0, reverted: 0, dropped: 0 };
  for (const row of rows) {
    summary[row.kind] += 1;
    const choice = choices[row.key] ?? defaultCompareChoice(row);
    if (row.kind === 'changed' && choice === 'original') summary.reverted += 1;
    if (row.kind === 'removed' && choice === 'original') summary.reverted += 1;
    if (row.kind === 'added' && choice === 'original') summary.dropped += 1;
  }
  return summary;
}
