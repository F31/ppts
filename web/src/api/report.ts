import type { ClientIdentity } from './identity';
import { getJSON } from './http';

// 导入报告的类型镜像 internal/project.ImportReport。
// 字段名保持后端 JSON 原名（camelCase），不再二次改名：这份数据只在报告页消费，
// 多一层映射只会让"后端加了一个字段、前端悄悄看不见"更容易发生。

export type ImportFeatures = {
  opaqueShapes: number;
  pictures: number;
  audioClips: number;
  videoClips: number;
  chartShapes: number;
  tableShapes: number;
  slidesWithTiming: number;
  pagesWithNotes: number;
  pageCount: number;
};

export type ImportFinding = {
  code: string;
  severity: 'warn' | 'info';
  count: number;
  pages?: number[];
};

export type ImportReportPage = {
  index: number;
  slideId: string;
  name?: string;
  hidden: boolean;
  notesChars: number;
  shapeCount: number;
  opaqueCount: number;
  flags?: string[];
};

export type ImportReport = {
  schemaVersion: string;
  parserName: string;
  parserVersion: string;
  pageCount: number;
  hiddenPages: number;
  pagesWithNotes: number;
  pagesWithoutNotes: number;
  features?: ImportFeatures;
  findings: ImportFinding[];
  diagnostics?: string[];
  pages: ImportReportPage[];
};

export type ImportReportRevision = {
  revisionNo: number;
  displayName: string;
  pageCount: number;
  parserVersion: string;
  createdAt: string;
};

export type ImportReportResponse = {
  revision: ImportReportRevision;
  report: ImportReport;
};

// getImportReport 读取某个源版本的导入报告。
// 409 + code=report_not_ready 表示解析还没产出；404 表示版本不存在。
// 两者的处置完全不同，调用方必须按 code 区分，不能只看"失败了"。
export async function getImportReport(
  identity: ClientIdentity,
  projectId: string,
  revisionNo: number
): Promise<ImportReportResponse> {
  return getJSON<ImportReportResponse>(
    identity,
    `/projects/${encodeURIComponent(projectId)}/revisions/${encodeURIComponent(String(revisionNo))}/import-report`
  );
}
