package api

import (
	"testing"

	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/internal/artifact"
)

func TestExportFormatMapsAudioPPTX(t *testing.T) {
	format, err := exportFormat(pptsv1.ArtifactFormat_ARTIFACT_FORMAT_AUDIO_PPTX)
	if err != nil || format != artifact.FormatAudioPPTX {
		t.Fatalf("exportFormat = %q, %v", format, err)
	}
	if got := toProtoExportFormat(artifact.FormatAudioPPTX); got != pptsv1.ArtifactFormat_ARTIFACT_FORMAT_AUDIO_PPTX {
		t.Fatalf("toProtoExportFormat = %v", got)
	}
}
