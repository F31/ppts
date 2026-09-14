-- G2-1: structured provenance anchors for generated narration segments.
-- source_refs text[] remains for backward compatibility; source_anchors carries
-- slide/shape/source/confidence details used by AI quality gates.

ALTER TABLE narration_segments
  ADD COLUMN IF NOT EXISTS source_anchors jsonb NOT NULL DEFAULT '[]'::jsonb;
