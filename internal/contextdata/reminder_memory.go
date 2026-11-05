package contextdata

import "sort"

// Source membership and storage IDs are provenance, not changes to a reminder.
// Keep the acknowledged consumption ID when storage consolidates old sources.
func reminderHistory(seen map[string]Item) map[string][]Item {
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bySystemID := map[string][]Item{}
	for _, key := range keys {
		old := seen[key]
		if id := Str(old.Data, "id"); id != "" {
			bySystemID[id] = append(bySystemID[id], old)
		}
	}
	return bySystemID
}

func reminderConsumption(item Item, history map[string][]Item) (Item, Item) {
	item.Version = reminderContentVersion(item.Data)
	var previous Item
	versions := map[string]bool{}
	for _, old := range history[Str(item.Data, "id")] {
		old.Version = reminderContentVersion(old.Data)
		versions[old.Version] = true
		if previous.ID == "" || old.ID == item.ID {
			previous = old
		}

	}
	// Legacy checkpoints have no acknowledgment ordering. Conflicting copies
	// cannot prove the current state was the last state written to memory.
	if len(versions) > 1 {
		previous.Version = ""
	}
	if previous.ID != "" {
		item.ID = previous.ID
	}
	return item, previous
}

func reminderContentVersion(data map[string]any) string {
	content := make(map[string]any, len(data))
	for key, value := range data {
		if key != "sourceId" && key != "sourceIds" && key != "recordId" {
			content[key] = value
		}
	}
	return Hash(content)
}
