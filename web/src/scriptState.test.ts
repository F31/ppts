import { scriptControlsFor, type ScriptUiState } from './scriptState';

const states: ScriptUiState[] = ['missing', 'ai_clean', 'edited', 'confirmed', 'locked'];

for (const state of states) {
  const controls = scriptControlsFor(state);
  if (!controls.notesEditable) throw new Error(`${state}: notes must always be editable`);
}

const matrix: Record<ScriptUiState, Partial<ReturnType<typeof scriptControlsFor>>> = {
  missing: { scriptEditable: false, rewriteDisabled: true, markerDisabled: true, generateDisabled: false },
  ai_clean: { scriptEditable: true, rewriteDisabled: false, markerDisabled: true, generateDisabled: false, generateNeedsConfirm: false },
  edited: { scriptEditable: true, rewriteDisabled: false, markerDisabled: true, generateDisabled: false, generateNeedsConfirm: true },
  confirmed: { scriptEditable: true, rewriteDisabled: false, markerDisabled: true, generateDisabled: false, generateNeedsConfirm: false },
  locked: { scriptEditable: true, rewriteDisabled: false, markerDisabled: true, generateDisabled: false }
};

for (const state of states) {
  const controls = scriptControlsFor(state);
  for (const [key, value] of Object.entries(matrix[state])) {
    if (controls[key as keyof typeof controls] !== value) throw new Error(`${state}.${key}: expected ${value}`);
  }
}
