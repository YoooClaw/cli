package transfercloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/YoooClaw/cli/internal/transferjob"
)

func TestEventContract(t *testing.T) {
	for _, status := range []int{200, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/fulfillment/transfer/events" || r.Header.Get("X-Api-Key-Id") != "cli-key" {
					t.Error("wrong endpoint/auth")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["eventType"] != "IMPORT_STARTED" {
					t.Error(body)
				}
				w.WriteHeader(status)
				w.Write([]byte(`{"success":true,"code":"000000","data":null}`))
			})
			err := c.ReportEvent(context.Background(), "https://api.example/fulfillment/transfer", transferjob.Event{TransferID: "tr_test", EventType: "IMPORT_STARTED"})
			if (err == nil) != (status == 200) {
				t.Fatal(err)
			}
			if status != 200 {
				var e *eventError
				if !errors.As(err, &e) || e.Permanent() != (status == 403) {
					t.Fatal(err)
				}
			}
		})
	}
}
