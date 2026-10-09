package app

import (
	"context"

	"github.com/F31/ppts/internal/integrations/tts"
)

// TextPreparationFactory resolves a tenant/language-specific text capability.
// Implementations own rule loading, caching and component selection.
type TextPreparationFactory interface {
	Resolve(context.Context, string, string) (TextPreparer, error)
}

// TextPreparer is the narration-facing contract, independent of textnorm types.
type TextPreparer interface {
	Prepare(context.Context, string, string) (PreparedText, error)
	Display(string) string
}

type PreparedText struct {
	Text   string
	Pauses []tts.Pause
	// DisplayToSpoken maps display rune positions to effective spoken positions.
	// nil means that precise mapping is unavailable.
	DisplayToSpoken []int
}

func (h *NarrationHandler) WithTextPreparation(f TextPreparationFactory) *NarrationHandler {
	h.textNorm = f
	return h
}
