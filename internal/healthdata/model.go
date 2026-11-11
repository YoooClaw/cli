// Package healthdata stores validated v3 health day snapshots.
package healthdata

import (
	"encoding/json"
	"time"
)

// dayInfo is the validation outcome for one day, aligned with DailyBatch.Days.
// A nil err means the day is writable; dropped and warnings describe records
// that were discarded while the rest of the day survived.
type dayInfo struct {
	err      error
	dropped  int
	warnings []string
	count    int
}

type Batch struct {
	daily        *DailyBatch
	collectedAt  time.Time
	dayInfo      []dayInfo
	ItemCount    int
	DeletedCount int
}

func (b Batch) CollectedAt() time.Time { return b.collectedAt }
func Prepare(raw json.RawMessage) (Batch, error) {
	daily, collected, infos, count, err := prepareDaily(raw)
	if err != nil {
		return Batch{}, err
	}
	return Batch{daily: daily, collectedAt: collected, dayInfo: infos, ItemCount: count}, nil
}
