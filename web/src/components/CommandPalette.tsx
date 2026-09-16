import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { navigate } from '../router';
import { useI18n } from '../i18n';
import { useDialogA11y } from '../a11y';
import { can, type Capability } from '../permissions';
import { primaryNav, settingsMenus } from '../AppShell';
import type { Role } from '../types';

type Command = {
  id: string;
  label: string;
  to: string;
  section: 'nav' | 'actions';
  icon: string;
};

function buildCommands(role: Role | undefined, roleReady: boolean, t: (key: string) => string): Command[] {
  const cmds: Command[] = [];
  const settingsHome = settingsMenus.find((m) => can(role, m.need))?.to ?? '/settings/usage';
  for (const item of primaryNav) {
    const to = item.key === 'settings' ? settingsHome : item.to;
    cmds.push({ id: `nav:${item.key}`, label: t(item.labelKey), to, section: 'nav', icon: item.icon });
  }
  for (const item of settingsMenus) {
    if (roleReady && can(role, item.need)) {
      cmds.push({ id: `settings:${item.key}`, label: t(item.labelKey), to: item.to, section: 'nav', icon: '🔧' });
    }
  }
  cmds.push({ id: 'action:newProject', label: t('palette.newProject'), to: '/projects', section: 'actions', icon: '➕' });
  cmds.push({ id: 'action:explore', label: t('palette.explore'), to: '/explore', section: 'actions', icon: '🌐' });
  return cmds;
}

export function CommandPalette({
  open,
  onClose,
  role,
  roleReady = true,
}: {
  open: boolean;
  onClose: () => void;
  role?: Role;
  roleReady?: boolean;
}) {
  const { t } = useI18n();
  const dialogRef = useDialogA11y<HTMLDivElement>(onClose, open);
  const [query, setQuery] = useState('');
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  // 打开时重置查询与选中项。
  useEffect(() => {
    if (open) {
      setQuery('');
      setSel(0);
    }
  }, [open]);

  const commands = useMemo(() => buildCommands(role, roleReady, t), [role, roleReady, t]);
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter((c) => c.label.toLowerCase().includes(q));
  }, [commands, query]);

  if (!open) return null;

  const run = (c: Command) => {
    navigate(c.to);
    onClose();
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSel((s) => Math.min(s + 1, filtered.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSel((s) => Math.max(s - 1, 0));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const c = filtered[sel];
      if (c) run(c);
    }
  };

  let lastSection = '';
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={t('palette.title')} ref={dialogRef}>
      <section className="modal-card command-palette" onKeyDown={onKeyDown}>
        <header className="palette-header">
          <input
            className="palette-input"
            value={query}
            placeholder={t('palette.placeholder')}
            aria-label={t('palette.placeholder')}
            aria-controls="palette-list"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => {
              setQuery(e.currentTarget.value);
              setSel(0);
            }}
          />
        </header>
        <div className="palette-list" id="palette-list" role="listbox" aria-label={t('palette.title')} ref={listRef}>
          {filtered.length === 0 && <p className="palette-empty">{t('palette.empty')}</p>}
          {filtered.map((c, i) => {
            const showHeader = c.section !== lastSection;
            lastSection = c.section;
            return (
              <Fragment key={c.id}>
                {showHeader && <div className="palette-group">{t(c.section === 'nav' ? 'palette.nav' : 'palette.actions')}</div>}
                <button
                  type="button"
                  role="option"
                  aria-selected={i === sel}
                  className={`palette-item ${i === sel ? 'selected' : ''}`}
                  onMouseEnter={() => setSel(i)}
                  onClick={() => run(c)}
                >
                  <span className="palette-icon" aria-hidden="true">{c.icon}</span>
                  <span className="palette-label">{c.label}</span>
                </button>
              </Fragment>
            );
          })}
        </div>
      </section>
    </div>
  );
}
