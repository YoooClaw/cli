package contextdata

import (
	"fmt"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"reflect"
	"strings"
	"testing"
)

func TestCommitGuidanceUsesFrozenRemainderAndSurvivesRetry(t *testing.T) {
	for _, count := range []int{1, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			p := paths.ForRoot(t.TempDir(), "test")
			d := Dataset{Connected: true}
			for i := 0; i < count; i++ {
				d.Items = append(d.Items, Item{ID: fmt.Sprint(i), Date: "1997-07-16", Version: "v1", Data: map[string]any{"id": fmt.Sprint(i), "title": "Test"}})
			}
			read := func() (Dataset, error) { return d, nil }
			batch, e := NextWithReader(p, "reminders", Options{All: true}, false, read)
			if e != nil {
				t.Fatal(e)
			}
			token := batch["token"].(string)
			// New data after fetch must not change the receipt's frozen remainder.
			d.Items = append(d.Items, Item{ID: "late", Date: "1997-07-16", Data: map[string]any{"id": "late"}})
			result, e := Commit(p, "reminders", token)
			if e != nil {
				t.Fatal(e)
			}
			action := "stop"
			if count > 20 {
				action = "continue_if_within_limit"
			}
			if result["remainingInScope"] != batch["remainingInScope"] || result["nextAction"] != action || result["remainingBasis"] != "batch-fetch-time" || result["message"] == "" {
				t.Fatal(result)
			}
			if _, e = NextWithReader(p, "reminders", Options{All: true}, false, read); e != nil {
				t.Fatal(e)
			}
			retry, e := Commit(p, "reminders", token)
			if e != nil || !reflect.DeepEqual(result, retry) {
				t.Fatal(result, retry, e)
			}
			path, _ := statePath(p, "reminders")
			state, e := loadState(path)
			if e != nil || state.Pending == nil {
				t.Fatal("retry cleared newer pending batch", e)
			}
		})
	}
}

func TestLegacyCommitGuidanceDoesNotInventRemaining(t *testing.T) {
	p := paths.ForRoot(t.TempDir(), "test")
	token := strings.Repeat("a", 48)
	path, _ := statePath(p, "health-data")
	if e := fsutil.WriteJSON(path, memoryState{Seen: map[string]Item{}, LastCommit: token}, fsutil.SecretFileMode); e != nil {
		t.Fatal(e)
	}
	r, e := Commit(p, "health-data", token)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := r["remainingInScope"]; ok || r["nextAction"] != "follow_previous_batch" {
		t.Fatal(r)
	}
}
