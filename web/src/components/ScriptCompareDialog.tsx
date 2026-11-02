import { useEffect, useMemo, useState } from 'react';
import { useDialogA11y } from '../a11y';
import { useI18n } from '../i18n';
import {
  buildCompareRows,
  defaultCompareChoice,
  displaySentence,
  mergeCompareRows,
  summarizeCompare,
  type CompareChoice,
  type CompareRow
} from '../scriptCompare';
import type { ScriptCompareData } from '../api/scripts';

type Props = {
  data: ScriptCompareData | null;
  loading: boolean;
  error?: string;
  // canApply：当前用户可编辑讲稿（服务端 ScriptService.Update 的最低要求）。
  canApply: boolean;
  // segmentCount：本页段落数。>1 时整页采用会抹掉用户的分段节奏，故只读。
  segmentCount: number;
  onClose: () => void;
  onApply: (text: string) => void;
  onRetry?: () => void;
};

// ScriptCompareDialog 是 G2-2 的「原文 vs 建议稿」并排对照面板。
//
// 交互原则：默认全部采用建议稿（与"直接看当前讲稿"一致），用户**逐句**把某些说法
// 换回原文或把新增句删掉，再一次性写回本页讲稿。写回走编辑器既有的自动保存通道，
// 因此这里不做任何网络写请求——多一条写路径就多一份与乐观并发不一致的风险。
export function ScriptCompareDialog({ data, loading, error, canApply, segmentCount, onClose, onApply, onRetry }: Props) {
  const { t } = useI18n();
  const dialogRef = useDialogA11y<HTMLDivElement>(onClose);
  const rows = useMemo(() => (data ? buildCompareRows(data.originalText, data.currentText) : []), [data]);
  const [choices, setChoices] = useState<Record<string, CompareChoice>>({});

  // 数据变了就重置选择：对照的是另一份稿子，沿用旧选择会把用户没看过的句子改掉。
  useEffect(() => {
    const next: Record<string, CompareChoice> = {};
    for (const row of rows) next[row.key] = defaultCompareChoice(row);
    setChoices(next);
  }, [rows]);

  const merged = useMemo(() => mergeCompareRows(rows, choices), [rows, choices]);
  const summary = useMemo(() => summarizeCompare(rows, choices), [rows, choices]);
  const blocked = !canApply || segmentCount > 1 || !data || data.source === 'none' || rows.length === 0;
  const unchanged = data ? merged === data.currentText : true;

  const toggle = (row: CompareRow) => {
    setChoices((current) => {
      const now = current[row.key] ?? defaultCompareChoice(row);
      return { ...current, [row.key]: now === 'current' ? 'original' : 'current' };
    });
  };

  return (
    <div className="modal-backdrop" role="presentation" onClick={onClose}>
      <div
        ref={dialogRef}
        className="modal-card script-compare"
        role="dialog"
        aria-modal="true"
        aria-label={t('compare.title')}
        onClick={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <span className="eyebrow">{t('compare.eyebrow')}</span>
            <h2>{t('compare.title')}</h2>
            <p className="cell-sub">{t('compare.subtitle')}</p>
          </div>
          <button type="button" onClick={onClose} aria-label={t('common.close')}>{t('common.close')}</button>
        </header>

        {data && (
          <div className="compare-meta">
            <span className={`compare-chip source-${data.source}`}>{sourceLabel(data.source, t)}</span>
            <span className="compare-chip mode">{modeLabel(data.mode, t)}</span>
            <span className="compare-chip">{t('compare.version', { revision: data.revisionNo })}</span>
          </div>
        )}

        {loading && <p className="cell-sub">{t('common.loading')}</p>}
        {error && (
          <div role="alert">
            <p className="form-error">{error}</p>
            {onRetry && <button type="button" onClick={onRetry}>{t('common.retry')}</button>}
          </div>
        )}
        {data && data.source === 'none' && !loading && <p className="empty-state">{t('compare.noOriginal')}</p>}

        {data && rows.length > 0 && (
          <>
            <div className="compare-head" aria-hidden="true">
              <span>{t('compare.originalColumn')}</span>
              <span>{t('compare.currentColumn')}</span>
              <span>{t('compare.choiceColumn')}</span>
            </div>
            <div className="compare-list">
              {rows.map((row) => {
                const choice = choices[row.key] ?? defaultCompareChoice(row);
                const chosen = choice === 'original' ? 'original' : 'current';
                return (
                  <article key={row.key} className={`compare-row ${row.kind} chosen-${chosen}`}>
                    <p className={`compare-text original ${row.original ? '' : 'blank'}`}>
                      {row.original ? displaySentence(row.original) : '—'}
                    </p>
                    <p className={`compare-text current ${row.current ? '' : 'blank'}`}>
                      {row.current ? displaySentence(row.current) : '—'}
                    </p>
                    <div className="compare-actions">
                      <span className={`compare-state ${row.kind} chosen-${chosen}`}>
                        {stateLabel(row.kind, chosen, t)}
                      </span>
                      {row.kind !== 'same' && (
                        <button type="button" className="button-ghost" onClick={() => toggle(row)}>
                          {actionLabel(row.kind, chosen, t)}
                        </button>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          </>
        )}

        {data && segmentCount > 1 && <p className="compare-warning">{t('compare.multiSegment', { count: segmentCount })}</p>}
        {data && !canApply && <p className="compare-warning">{t('compare.readOnly')}</p>}

        <footer className="modal-actions">
          {data && rows.length > 0 && (
            <p className="compare-summary">
              {t('compare.summary', {
                total: summary.total,
                changed: summary.changed,
                added: summary.added,
                removed: summary.removed,
                reverted: summary.reverted,
                dropped: summary.dropped
              })}
            </p>
          )}
          <button type="button" onClick={onClose}>{t('common.cancel')}</button>
          <button
            type="button"
            className="button-primary"
            disabled={blocked || unchanged}
            title={unchanged && !blocked ? t('compare.nothingToApply') : blocked ? t('compare.applyBlocked') : undefined}
            onClick={() => onApply(merged)}
          >
            {t('compare.apply')}
          </button>
        </footer>
      </div>
    </div>
  );
}

function sourceLabel(source: string, t: (key: string) => string): string {
  switch (source) {
    case 'page':
      return t('compare.sourcePage');
    case 'notes':
      return t('compare.sourceNotes');
    case 'custom':
      return t('compare.sourceCustom');
    default:
      return t('compare.sourceNone');
  }
}

function modeLabel(mode: string, t: (key: string) => string): string {
  switch (mode) {
    case 'polish':
      return t('compare.modePolish');
    case 'ai_generated':
      return t('compare.modeAI');
    case 'original':
      return t('compare.modeOriginal');
    default:
      return t('compare.modeUnknown');
  }
}

function stateLabel(kind: CompareRow['kind'], chosen: CompareChoice, t: (key: string) => string): string {
  if (kind === 'same') return t('compare.stateSame');
  if (kind === 'changed') return chosen === 'original' ? t('compare.stateReverted') : t('compare.statePolished');
  if (kind === 'added') return chosen === 'original' ? t('compare.stateDropped') : t('compare.stateAdded');
  return chosen === 'original' ? t('compare.stateRestored') : t('compare.stateRemoved');
}

function actionLabel(kind: CompareRow['kind'], chosen: CompareChoice, t: (key: string) => string): string {
  if (kind === 'changed') return chosen === 'original' ? t('compare.useCurrent') : t('compare.useOriginal');
  if (kind === 'added') return chosen === 'original' ? t('compare.keepLine') : t('compare.dropLine');
  return chosen === 'original' ? t('compare.deleteAgain') : t('compare.restoreLine');
}
