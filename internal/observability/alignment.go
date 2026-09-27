package observability

import "expvar"

var ttsVADAlignmentTotal = expvar.NewMap("ppts_tts_vad_alignment_total")

// TTSVADAlignment records one VAD computation, including cache recomputation.
// Callers supply fixed method/reason values, never text, IDs or error messages.
func TTSVADAlignment(method, reason string) {
	ttsVADAlignmentTotal.Add("method="+clean(method)+",reason="+clean(reason), 1)
}
