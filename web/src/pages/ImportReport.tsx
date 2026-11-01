import { useCallback, useEffect, useState } from 'react';
import {
  getImportReport,
  getProject,
  getSourceRevisions,
  type ClientIdentity,
  type ImportReport,
  type ImportReportRevision,
  type ImportReportPage
} from '../api';
import { describeApiError } from '../apiError';
import { useI18n } from '../i18n';
import { Link } from '../router';
import type { Project } from '../types';
import {
  classifyReportError,
  describeFlag,
  humanizePages,
  notesCoverage,
  orderFindings,
  pageCountMismatch,
  type ReportFailure
} from '../importReport';

type State =
  | { phase: 'loading' }
  | { phase: 'ready'; revision: ImportReportRevision; report: ImportReport }
  | { phase: 'failed'; failure: ReportFailure; message: string };

type Props = {
  identity: ClientIdentity;
  projectId: string;
  revisionNo?: number;
};

// ImportReportPage 是"这份 PPT 被读成了什么"的页面。
//
// 它只呈现导入时那一次解析的产物，不重新解析、不合并应用内讲稿：
// 用户在这里确认的是"文件本身有什么问题"，若把编辑后的讲稿混进来，
// "缺备注"之类的事实就会被后续补写悄悄抹掉，报告也就失去了意义。
export function ImportReportPage({ identity, projectId, revisionNo }: Props) {
  const { t } = useI18n();
  const [project, setProject] = useState<Project | null>(null);
  const [state, setState] = useState<State>({ phase: 'loading' });
  const [reloadKey, setReloadKey] = useState(0);

  const load = useCallback(async () => {
    setState({ phase: 'loading' });
    try {
      const proj = await getProject(identity, projectId);
      setProject(proj);
      // 未带 rev 时按当前生效版本查看：报告页最常见的入口就是"刚导入完，看看读成什么样"。
      let rev = revisionNo && revisionNo > 0 ? revisionNo : proj.currentRevision;
      if (!rev || rev <= 0) {
        const history = await getSourceRevisions(identity, projectId);
        rev = history.currentRevision;
      }
      if (!rev || rev <= 0) {
        setState({ phase: 'failed', failure: 'revision_missing', message: t('report.noRevision') });
        return;
      }
      const data = await getImportReport(identity, projectId, rev);
      setState({ phase: 'ready', revision: data.revision, report: data.report });
    } catch (error) {
      const code = (error as { code?: string } | null)?.code;
      const failure = classifyReportError(typeof code === 'string' ? code : undefined);
      setState({ phase: 'failed', failure, message: failureMessage(failure, error, t) });
    }
  }, [identity, projectId, revisionNo, t]);

  useEffect(() => {
    void load();
  }, [load, reloadKey]);

  const retryable = state.phase === 'failed' && (state.failure === 'not_ready' || state.failure === 'unknown');
  const editorHref = `/projects/${projectId}/editor${revisionNo && revisionNo > 0 ? `?rev=${revisionNo}` : ''}`;

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('report.eyebrow')}</span>
          <h1>{project?.title ?? t('report.title')}</h1>
          <p className="page-sub">
            {state.phase === 'ready'
              ? t('report.subtitle', {
                  rev: state.revision.revisionNo,
                  name: state.revision.displayName || t('report.unnamedVersion'),
                  parser: state.report.parserName,
                  version: state.report.parserVersion
                })
              : t('report.subtitleLoading')}
          </p>
        </div>
        <div className="page-actions">
          <button type="button" className="button-ghost" onClick={() => setReloadKey((k) => k + 1)}>
            {t('common.refresh')}
          </button>
          <Link to={editorHref} className="button-ghost">
            {t('report.backToEditor')}
          </Link>
        </div>
      </section>

      {state.phase === 'loading' && <p className="empty-state">{t('common.loading')}</p>}

      {state.phase === 'failed' && (
        <section className="panel">
          <p className="api-status error">{state.message}</p>
          {retryable && (
            <div className="draft-actions">
              <button type="button" className="primary" onClick={() => setReloadKey((k) => k + 1)}>
                {t('common.retry')}
              </button>
            </div>
          )}
        </section>
      )}

      {state.phase === 'ready' && (
        <ReadyView report={state.report} revision={state.revision} />
      )}
    </div>
  );
}

function failureMessage(failure: ReportFailure, error: unknown, t: (key: string, values?: Record<string, string | number>) => string): string {
  switch (failure) {
    case 'not_ready':
      return t('report.notReady');
    case 'revision_missing':
      return t('report.revisionMissing');
    case 'corrupt':
      return t('report.corrupt');
    case 'invalid':
      return t('report.invalidRevision');
    case 'forbidden':
      return t('report.forbidden');
    default:
      return describeApiError(error, t('report.loadFailed'), t);
  }
}

function ReadyView({ report, revision }: { report: ImportReport; revision: ImportReportRevision }) {
  const { t } = useI18n();
  const findings = orderFindings(report.findings ?? []);
  const coverage = notesCoverage(report.pageCount, report.pagesWithNotes);
  const mismatch = pageCountMismatch(report.pageCount, revision.pageCount);
  const opaque = report.features?.opaqueShapes ?? 0;

  return (
    <>
      {mismatch && (
        <p className="report-alert" role="status">
          {t('report.mismatch', { report: report.pageCount, record: revision.pageCount })}
        </p>
      )}

      <div className="stat-grid">
        <div className="stat-card">
          <span className="stat-label">{t('report.statPages')}</span>
          <span className="stat-value">{report.pageCount}</span>
          <span className="stat-note">{t('report.statPagesNote', { hidden: report.hiddenPages })}</span>
        </div>
        <div className="stat-card">
          <span className="stat-label">{t('report.statNotes')}</span>
          <span className="stat-value">{report.pagesWithNotes}</span>
          <span className="stat-note">{t('report.statNotesNote', { percent: coverage })}</span>
        </div>
        <div className="stat-card">
          <span className="stat-label">{t('report.statNoNotes')}</span>
          <span className="stat-value">{report.pagesWithoutNotes}</span>
          <span className="stat-note">{t('report.statNoNotesNote')}</span>
        </div>
        <div className="stat-card">
          <span className="stat-label">{t('report.statOpaque')}</span>
          <span className="stat-value">{opaque}</span>
          <span className="stat-note">{t('report.statOpaqueNote')}</span>
        </div>
      </div>

      <section className="panel">
        <div className="table-head">
          <h2>{t('report.findingsTitle')}</h2>
          <span className="stats-note">{t('report.findingsNote')}</span>
        </div>
        {findings.length === 0 ? (
          <p className="empty-state">{t('report.noFindings')}</p>
        ) : (
          <ul className="report-findings">
            {findings.map((finding) => {
              const pages = humanizePages(finding.pages);
              return (
                <li key={finding.code} className={`report-finding ${finding.severity}`}>
                  <span className="report-finding-badge">
                    {finding.severity === 'warn' ? t('report.severityWarn') : t('report.severityInfo')}
                  </span>
                  <div>
                    <p className="report-finding-text">{findingText(finding.code, finding.count, t)}</p>
                    {pages.text && (
                      <p className="report-finding-pages">
                        {t('report.findingPages', { pages: pages.text })}
                        {/* 只列前几页时必须说清总数，否则"等共 6 页"会被读成"一共 6 页"——
                            而实际受影响的是 14 页。 */}
                        {pages.truncated ? t('report.findingPagesAll', { count: finding.pages?.length ?? 0 }) : ''}
                      </p>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section className="panel">
        <div className="table-head">
          <h2>{t('report.pagesTitle')}</h2>
          <span className="stats-note">{t('report.pagesNote')}</span>
        </div>
        <table className="data-table report-pages">
            <thead>
              <tr>
                <th>{t('report.colIndex')}</th>
                <th>{t('report.colName')}</th>
                <th>{t('report.colFlags')}</th>
                <th>{t('report.colShapes')}</th>
                <th>{t('report.colNotes')}</th>
              </tr>
            </thead>
            <tbody>
              {report.pages.map((page) => (
                <PageRow key={page.slideId || String(page.index)} page={page} />
              ))}
            </tbody>
        </table>
      </section>

      {report.diagnostics && report.diagnostics.length > 0 && (
        <section className="panel">
          <div className="table-head">
            <h2>{t('report.diagnosticsTitle')}</h2>
            <span className="stats-note">{t('report.diagnosticsNote')}</span>
          </div>
          <ul className="report-diagnostics">
            {report.diagnostics.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        </section>
      )}

      <p className="panel-note">{t('report.footnote')}</p>
    </>
  );
}

function PageRow({ page }: { page: ImportReportPage }) {
  const { t } = useI18n();
  return (
    <tr className={page.hidden ? 'report-page-hidden' : undefined}>
      <td>{page.index + 1}</td>
      <td>{page.name || <span className="muted">{t('report.unnamedPage')}</span>}</td>
      <td>
        <span className="report-flags">
          {(page.flags ?? []).length === 0 ? (
            <span className="muted">—</span>
          ) : (
            (page.flags ?? []).map((flag) => <FlagChip key={flag} flag={flag} />)
          )}
        </span>
      </td>
      <td>{page.opaqueCount > 0 ? `${page.shapeCount} (${t('report.opaqueInPage', { count: page.opaqueCount })})` : page.shapeCount}</td>
      <td>{page.notesChars > 0 ? t('report.notesChars', { count: page.notesChars }) : <span className="muted">{t('report.noNotes')}</span>}</td>
    </tr>
  );
}

function FlagChip({ flag }: { flag: string }) {
  const { t } = useI18n();
  const { kind, detail } = describeFlag(flag);
  if (kind === 'unknown') {
    return (
      <span className="report-flag unknown" title={t('report.flagUnknownHint')}>
        {detail}
      </span>
    );
  }
  const label = kind === 'opaqueShape' ? t('report.flag.opaqueShape', { name: detail }) : t(`report.flag.${kind}`);
  return <span className={`report-flag ${kind}`}>{label}</span>;
}

function findingText(code: string, count: number, t: (key: string, values?: Record<string, string | number>) => string): string {
  const key = `report.finding.${code}`;
  const text = t(key, { count });
  // i18n 未登记的条目代码按原文展示：后端先上线、前端还没跟时，
  // 宁可露出一行英文代码，也不能把这条风险从界面上吞掉。
  return text === key ? t('report.finding.unknown', { code, count }) : text;
}
