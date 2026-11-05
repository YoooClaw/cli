package reminders

import (
	"errors"
	"slices"
	"time"
)

func recordSources(row Record) []string {
	if len(row.SourceIDs) != 0 {
		return row.SourceIDs
	}
	return []string{row.SourceID}
}

func withSources(row Record, sources []string) Record {
	row.SourceIDs = sources
	if len(sources) == 1 && sources[0] == row.SourceID {
		row.SourceIDs = nil
	}
	return row
}

func validSources(source string, sources []string, state State) bool {
	if _, ok := state.Sources[source]; !ok {
		return false
	}
	for i, id := range sources {
		if _, ok := state.Sources[id]; !ok || i > 0 && sources[i-1] >= id {
			return false
		}
	}
	return true
}

// Older stores may have one row per source. Their individual upload times
// were not stored; use the last accepted snapshot for each source to break ties.
func mergeStoredRecord(a, b Record, state State) Record {
	sources := append(slices.Clone(recordSources(a)), recordSources(b)...)
	slices.Sort(sources)
	at, _ := time.Parse(time.RFC3339Nano, state.Sources[a.SourceID].LastCollectedAt)
	bt, _ := time.Parse(time.RFC3339Nano, state.Sources[b.SourceID].LastCollectedAt)
	if bt.After(at) || bt.Equal(at) && b.RecordID < a.RecordID {
		a = b
	}
	return withSources(a, slices.Compact(sources))
}

func mergeSnapshot(previous []Record, p Payload, state State) ([]Record, error) {
	byID := make(map[string]Record, len(previous))
	for _, row := range previous {
		if !validSources(row.SourceID, row.SourceIDs, state) {
			return nil, errors.New("invalid stored reminder sources")
		}
		if other, ok := byID[row.ID]; ok {
			row = mergeStoredRecord(other, row, state)
		}
		byID[row.ID] = row
	}

	rows := make([]Record, 0, len(byID))
	for _, item := range p.Items {
		row := Record{Item: item, SourceID: p.SourceID, RecordID: itemRecordID(item.ID)}
		if old, ok := byID[item.ID]; ok {
			sources := recordSources(old)
			at, found := slices.BinarySearch(sources, p.SourceID)
			if !found {
				sources = slices.Insert(sources, at, p.SourceID)
			}
			row = withSources(row, sources)
			if validRecordID(old.RecordID, row.SourceID, row.ID) {
				row.RecordID = old.RecordID
			}
			delete(byID, item.ID)
		}
		rows = append(rows, row)
	}
	for _, row := range byID {
		sources := recordSources(row)
		if at, found := slices.BinarySearch(sources, p.SourceID); found {
			sources = slices.Delete(sources, at, at+1)
		}
		if len(sources) > 0 {
			rows = append(rows, withSources(row, sources))
		}
	}
	return rows, nil
}
