// Package llm defines the text/vision-generation ports used by G2 script understanding.
package llm

import (
	"context"
	"time"
)

// RewriteRequest is a deterministic text rewriting request. The caller provides
// the source text and any safety constraints (numbers/units/model names to keep).
type RewriteRequest struct {
	LogicalOpID  string
	Mode         string // polish | ai_generated
	Language     string
	SourceText   string
	Instructions string
}

// RewriteResult is the provider response plus lightweight cost metadata.
type RewriteResult struct {
	Text              string
	ProviderRequestID string
	PromptTokens      int64
	CompletionTokens  int64
	Model             string
	Warnings          []string
}

// TextRewriter is the narrow LLM port used by ScriptDraftHandler.
type TextRewriter interface {
	Rewrite(ctx context.Context, req RewriteRequest) (RewriteResult, error)
}

// VisualExtractRequest asks a vision model to extract visible facts from one slide image.
type VisualExtractRequest struct {
	LogicalOpID string
	Language    string
	SlideID     string
	ImagePNG    []byte
}

// VisualAnchor is a model-observed source fact. It is deliberately low-trust:
// callers should mark it below structural confidence and use it as evidence to review.
//
// Two roles are distinguished:
//   - text facts (Kind "visual_text"): visible literal text (numbers/units/models/
//     dates/labels) that may enter the validation baseline like structural text;
//   - scene descriptions (Kind "visual_scene"): what the image shows (objects,
//     layout, relation to the topic). Scene text is low-confidence, non-deterministic
//     free-form prose, so callers must NOT put it into a strict numeric-preservation
//     baseline (M11 lesson) — it is prompt-only context.
type VisualAnchor struct {
	Kind       string
	Raw        string
	Confidence float64
	// Scene 是「这张图展示什么」的自由描述（可选；非文本类事实）。不进校验基准。
	Scene string
}

// VisionExtractor is the narrow visual-channel port used by ScriptDraftHandler.
type VisionExtractor interface {
	ExtractVisual(ctx context.Context, req VisualExtractRequest) ([]VisualAnchor, error)
}

// RetryableError marks temporary provider failures (timeouts, 429, 5xx).
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }

func (e *RetryableError) Unwrap() error { return e.Err }
