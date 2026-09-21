export type ScriptUiState = 'missing' | 'ai_clean' | 'edited' | 'confirmed' | 'locked';

export type ScriptControls = {
  notesEditable: boolean;
  scriptEditable: boolean;
  rewriteDisabled: boolean;
  markerDisabled: boolean;
  markerTitleKey: string;
  generateDisabled: boolean;
  generateLabelKey: string;
  generateNeedsConfirm: boolean;
};

export function scriptControlsFor(state: ScriptUiState): ScriptControls {
  const generated = state !== 'missing';
  const edited = state === 'edited';
  return {
    notesEditable: true,
    scriptEditable: generated,
    rewriteDisabled: !generated,
    markerDisabled: true,
    markerTitleKey: 'editor.markersSoon',
    generateDisabled: false,
    generateLabelKey: generated ? 'editor.regenerateNarration' : 'editor.generateScript',
    generateNeedsConfirm: edited
  };
}

export function stateFromScriptStatus(status: 'draft' | 'approved' | 'locked' | undefined): ScriptUiState {
  if (!status) return 'missing';
  return 'ai_clean';
}
