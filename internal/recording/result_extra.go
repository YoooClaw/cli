package recording

import "encoding/json"

type resultWriteParamsWire ResultWriteParams

var resultWriteParamsWireNames = jsonFieldNames(resultWriteParamsWire{})

func (v *ResultWriteParams) UnmarshalJSON(data []byte) error {
	var typed resultWriteParamsWire
	extra, err := decodeMetadataExtra(data, &typed, resultWriteParamsWireNames)
	if err != nil {
		return err
	}
	*v = ResultWriteParams(typed)
	v.Extra = extra
	return nil
}
func (v ResultWriteParams) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(resultWriteParamsWire(v), v.Extra, resultWriteParamsWireNames)
}

type resultTranscriptWire ResultTranscript

var resultTranscriptWireNames = jsonFieldNames(resultTranscriptWire{})

func (v *ResultTranscript) UnmarshalJSON(data []byte) error {
	var typed resultTranscriptWire
	extra, err := decodeMetadataExtra(data, &typed, resultTranscriptWireNames)
	if err != nil {
		return err
	}
	*v = ResultTranscript(typed)
	v.Extra = extra
	return nil
}
func (v ResultTranscript) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(resultTranscriptWire(v), v.Extra, resultTranscriptWireNames)
}

type resultTranscriptSourceWire ResultTranscriptSource

var resultTranscriptSourceWireNames = jsonFieldNames(resultTranscriptSourceWire{})

func (v *ResultTranscriptSource) UnmarshalJSON(data []byte) error {
	var typed resultTranscriptSourceWire
	extra, err := decodeMetadataExtra(data, &typed, resultTranscriptSourceWireNames)
	if err != nil {
		return err
	}
	*v = ResultTranscriptSource(typed)
	v.Extra = extra
	return nil
}
func (v ResultTranscriptSource) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(resultTranscriptSourceWire(v), v.Extra, resultTranscriptSourceWireNames)
}

type resultSummaryWire ResultSummary

var resultSummaryWireNames = jsonFieldNames(resultSummaryWire{})

func (v *ResultSummary) UnmarshalJSON(data []byte) error {
	var typed resultSummaryWire
	extra, err := decodeMetadataExtra(data, &typed, resultSummaryWireNames)
	if err != nil {
		return err
	}
	*v = ResultSummary(typed)
	v.Extra = extra
	return nil
}
func (v ResultSummary) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(resultSummaryWire(v), v.Extra, resultSummaryWireNames)
}

type transcriptSourceWire TranscriptSource

var transcriptSourceWireNames = jsonFieldNames(transcriptSourceWire{})

func (v *TranscriptSource) UnmarshalJSON(data []byte) error {
	var typed transcriptSourceWire
	extra, err := decodeMetadataExtra(data, &typed, transcriptSourceWireNames)
	if err != nil {
		return err
	}
	*v = TranscriptSource(typed)
	v.Extra = extra
	return nil
}
func (v TranscriptSource) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(transcriptSourceWire(v), v.Extra, transcriptSourceWireNames)
}

type exportPayloadWire ExportPayload

var exportPayloadWireNames = jsonFieldNames(exportPayloadWire{})

func (v *ExportPayload) UnmarshalJSON(data []byte) error {
	var typed exportPayloadWire
	extra, err := decodeMetadataExtra(data, &typed, exportPayloadWireNames)
	if err != nil {
		return err
	}
	*v = ExportPayload(typed)
	v.Extra = extra
	return nil
}
func (v ExportPayload) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(exportPayloadWire(v), v.Extra, exportPayloadWireNames)
}

type markerWire Marker

var markerWireNames = jsonFieldNames(markerWire{})

func (v *Marker) UnmarshalJSON(data []byte) error {
	var typed markerWire
	extra, err := decodeMetadataExtra(data, &typed, markerWireNames)
	if err != nil {
		return err
	}
	*v = Marker(typed)
	v.Extra = extra
	return nil
}
func (v Marker) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(markerWire(v), v.Extra, markerWireNames)
}

// Markdown remains in the summary file; only structured data and extensions
// without an existing storage location are kept in the index.
func mergeSummaryData(current, incoming *ResultSummary) *ResultSummary {
	var out ResultSummary
	if current != nil && incoming.Markdown == "" && incoming.Structured == nil {
		out = *current
	}
	out.Extra = mergeMetadataExtra(out.Extra, incoming.Extra)
	if incoming.GeneratedAt != "" {
		out.GeneratedAt = incoming.GeneratedAt
	}
	if incoming.Format != "" {
		out.Format = incoming.Format
	}
	if incoming.Structured != nil {
		out.Structured = append(json.RawMessage(nil), incoming.Structured...)
	}
	out.Markdown = ""
	if len(out.Extra) == 0 && out.GeneratedAt == "" && out.Format == "" && out.Structured == nil {
		return nil
	}
	return &out
}
