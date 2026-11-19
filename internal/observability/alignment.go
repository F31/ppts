package observability

import "expvar"

var ttsVADAlignmentTotal = expvar.NewMap("ppts_tts_vad_alignment_total")

// TTSVADAlignment records one VAD computation, including cache recomputation.
// Callers supply fixed method/reason values, never text, IDs or error messages.
func TTSVADAlignment(method, reason string) {
	ttsVADAlignmentTotal.Add("method="+clean(method)+",reason="+clean(reason), 1)
}

var ttsVADAlignmentCacheTotal = expvar.NewMap("ppts_tts_vad_alignment_cache_total")

// TTSVADAlignmentCache records a shared-audio alignment recompute cache event
// (hit/miss) so cache effectiveness is observable.
func TTSVADAlignmentCache(event string) {
	ttsVADAlignmentCacheTotal.Add(clean(event), 1)
}

var subtitleCharsSourceTotal = expvar.NewMap("ppts_subtitle_chars_source_total")

// SubtitleCharsSource records how each subtitle cue's char-level timestamps were sourced.
// Values are fixed labels (exact_map / proportional_remap / exact_map_invalid_fallback / none),
// never text or IDs. It makes alignmentMethod + charsFallback observable per P2/P3.
func SubtitleCharsSource(source string) {
	subtitleCharsSourceTotal.Add(clean(source), 1)
}
