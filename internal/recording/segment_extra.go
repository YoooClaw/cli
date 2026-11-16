package recording

type resultTranscriptSegmentJSON ResultTranscriptSegment

var resultTranscriptSegmentJSONNames = jsonFieldNames(resultTranscriptSegmentJSON{})

func (s *ResultTranscriptSegment) UnmarshalJSON(data []byte) error {
	var typed resultTranscriptSegmentJSON
	extra, err := decodeMetadataExtra(data, &typed, resultTranscriptSegmentJSONNames)
	if err != nil {
		return err
	}
	*s = ResultTranscriptSegment(typed)
	s.Extra = extra
	return nil
}
func (s ResultTranscriptSegment) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(resultTranscriptSegmentJSON(s), s.Extra, resultTranscriptSegmentJSONNames)
}

type transcriptSegmentJSON TranscriptSegment

var transcriptSegmentJSONNames = jsonFieldNames(transcriptSegmentJSON{})

func (s *TranscriptSegment) UnmarshalJSON(data []byte) error {
	var typed transcriptSegmentJSON
	extra, err := decodeMetadataExtra(data, &typed, transcriptSegmentJSONNames)
	if err != nil {
		return err
	}
	*s = TranscriptSegment(typed)
	s.Extra = extra
	return nil
}
func (s TranscriptSegment) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(transcriptSegmentJSON(s), s.Extra, transcriptSegmentJSONNames)
}

type transcriptItemJSON TranscriptItem

var transcriptItemJSONNames = jsonFieldNames(transcriptItemJSON{})

func (s *TranscriptItem) UnmarshalJSON(data []byte) error {
	var typed transcriptItemJSON
	extra, err := decodeMetadataExtra(data, &typed, transcriptItemJSONNames)
	if err != nil {
		return err
	}
	*s = TranscriptItem(typed)
	s.Extra = extra
	return nil
}
func (s TranscriptItem) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(transcriptItemJSON(s), s.Extra, transcriptItemJSONNames)
}

type sourceTextItemJSON sourceTextItem

var sourceTextItemJSONNames = jsonFieldNames(sourceTextItemJSON{})

func (s *sourceTextItem) UnmarshalJSON(data []byte) error {
	var typed sourceTextItemJSON
	extra, err := decodeMetadataExtra(data, &typed, sourceTextItemJSONNames)
	if err != nil {
		return err
	}
	*s = sourceTextItem(typed)
	s.Extra = extra
	return nil
}
func (s sourceTextItem) MarshalJSON() ([]byte, error) {
	return encodeMetadataExtra(sourceTextItemJSON(s), s.Extra, sourceTextItemJSONNames)
}
