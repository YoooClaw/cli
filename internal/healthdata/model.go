// Package healthdata stores validated v3 health day snapshots.
package healthdata

import (
	"encoding/json"
	"time"
)

type Batch struct {
	daily        *DailyBatch
	collectedAt  time.Time
	ItemCount    int
	DeletedCount int
}

func (b Batch) CollectedAt() time.Time { return b.collectedAt }
func Prepare(raw json.RawMessage) (Batch, error) {
	daily, collected, count, err := prepareDaily(raw)
	if err != nil {
		return Batch{}, err
	}
	return Batch{daily: daily, collectedAt: collected, ItemCount: count}, nil
}
