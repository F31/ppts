import { useCallback, useEffect, useRef, useState } from 'react';
import { useI18n } from '../i18n';
import { useDialogA11y } from '../a11y';

export type DialogModeOption = { value: string; labelKey: string };

export type ConfirmPrompt = {
  kind: 'confirm';
  titleKey: string;
  messageKey: string;
  messageValues?: Record<string, string | number>;
  confirmKey?: string;
  cancelKey?: string;
  danger?: boolean;
  // modeOptions：确认框内嵌单选模式组；确认后返回 `mode:<value>`，供调用方读取所选模式。
  modeOptions?: DialogModeOption[];
  initialMode?: string;
};

export type PromptPrompt = {
  kind: 'prompt';
  titleKey: string;
  messageKey?: string;
  messageValues?: Record<string, string | number>;
  initial?: string;
  confirmKey?: string;
  cancelKey?: string;
};

export type DialogRequest = ConfirmPrompt | PromptPrompt;

export function useConfirmDialog<T extends DialogRequest = DialogRequest>() {
  const [request, setRequest] = useState<T | null>(null);
  const resolverRef = useRef<((value: string | null) => void) | null>(null);

  const ask = useCallback((prompt: T): Promise<string | null> => {
    return new Promise<string | null>((resolve) => {
      resolverRef.current = resolve;
      setRequest(prompt);
    });
  }, []);

  const settle = useCallback((value: string | null) => {
    resolverRef.current?.(value);
    resolverRef.current = null;
    setRequest(null);
  }, []);

  const dialog = request ? (
    <ConfirmDialog
      request={request}
      onCancel={() => settle(null)}
      onConfirm={(value) => settle(value)}
    />
  ) : null;

  return { ask, dialog, request };
}

function ConfirmDialog({
  request,
  onCancel,
  onConfirm
}: {
  request: DialogRequest;
  onCancel: () => void;
  onConfirm: (value: string | null) => void;
}) {
  const { t } = useI18n();
  const dialogRef = useDialogA11y<HTMLDivElement>(onCancel);
  const [value, setValue] = useState(request.kind === 'prompt' ? request.initial ?? '' : '');
  const [mode, setMode] = useState<string>(() => (request.kind === 'confirm' && request.modeOptions?.length ? request.initialMode ?? request.modeOptions[0].value : ''));
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (request.kind === 'prompt') inputRef.current?.focus();
  }, [request.kind]);

  const danger = request.kind === 'confirm' && request.danger === true;
  const confirmLabel = t(request.confirmKey ?? 'common.confirm');
  const cancelLabel = t(request.cancelKey ?? 'common.cancel');
  const modeOptions = request.kind === 'confirm' ? request.modeOptions ?? [] : [];

  const handleConfirm = () => {
    if (request.kind === 'prompt') {
      onConfirm(value);
      return;
    }
    if (modeOptions.length > 0) {
      onConfirm(`mode:${mode}`);
      return;
    }
    onConfirm('ok');
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={t(request.titleKey)} ref={dialogRef}>
      <section className="modal-card confirm-dialog">
        <header>
          <h2>{t(request.titleKey)}</h2>
        </header>
        <p className="confirm-message">
          {request.messageKey ? t(request.messageKey, request.messageValues) : ''}
        </p>
        {modeOptions.length > 0 && (
          <div className="confirm-modes" role="radiogroup" aria-label={t('editor.scriptMode')}>
            {modeOptions.map((option) => (
              <label key={option.value} className={mode === option.value ? 'selected' : ''}>
                <input type="radio" name="confirm-mode" value={option.value} checked={mode === option.value} onChange={() => setMode(option.value)} />
                <span>{t(option.labelKey)}</span>
              </label>
            ))}
          </div>
        )}
        {request.kind === 'prompt' && (
          <input
            ref={inputRef}
            className="confirm-input"
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onConfirm(value);
            }}
          />
        )}
        <footer className="confirm-actions">
          <button type="button" className="button-ghost" onClick={onCancel}>
            {cancelLabel}
          </button>
          <button
            type="button"
            className={danger ? 'button-primary danger' : 'button-primary'}
            onClick={handleConfirm}
          >
            {confirmLabel}
          </button>
        </footer>
      </section>
    </div>
  );
}
