package aitodo

import (
	"encoding/json"
	"testing"
)

func TestTimeConversion(t *testing.T) {
	for _, tc := range []struct {
		s    string
		day  bool
		want int64
	}{{"2026-09-11T15:00:00+08:00", false, 1789110000000}, {"2026-09-11T15:00+08:00", false, 1789110000000}, {"2026-09-11T07:00:00Z", false, 1789110000000}, {"2026-09-14", true, 1789344000000}, {"2026-09-14T15:00:00-07:00", true, 1789344000000}} {
		n, e := isoMillis(tc.s, tc.day)
		if e != nil || n != tc.want {
			t.Errorf("%s: %d %v", tc.s, n, e)
		}
	}
	for _, s := range []string{"", "2026-09-11", "2026-09-11T15:00:00", "2026-02-30T15:00:00+08:00", "2026-09-11T24:00:00Z", "2026-09-11T15:00:00+24:00", "2026-09-11T15:00:00+08:60", "2026-09-11T15:00:00-00:00"} {
		if _, e := isoMillis(s, false); e == nil {
			t.Error("accepted", s)
		}
	}
}
func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		a string
		p Fields
	}{{"update", Fields{"todoId": "1"}}, {"update", Fields{"todoId": "1", "isDone": "false"}}, {"update", Fields{"todoId": "1", "isFullDay": false}}, {"create", Fields{"title": "x", "dueAt": nil, "isFullDay": true}}, {"get", Fields{"todoId": json.Number("9007199254740993")}}, {"list", Fields{"pageNo": nil}}, {"delete", Fields{"todoIds": []string{"1"}, "confirmed": false}}, {"delete", Fields{"todoIds": []string{}, "confirmed": true}}, {"list", Fields{"startTime": 3, "endTime": 2}}, {"list", Fields{"unknown": true}}} {
		if _, e := Validate(tc.a, tc.p); e == nil {
			t.Errorf("accepted %s %#v", tc.a, tc.p)
		}
	}
	p, e := Validate("update", Fields{"todoId": "1", "dueAt": nil, "isDone": false})
	if e != nil || p["dueAt"] != nil || p["isDone"] != false || rawHas(p, "title") {
		t.Fatal(p, e)
	}
	p, e = Validate("list", Fields{"isDone": nil, "pageSize": nil})
	if e != nil || p["isDone"] != nil || !sameNumber(p["pageSize"], 20) {
		t.Fatal(p, e)
	}
}

func TestInferMissingAllDay(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		for _, tc := range []struct {
			due  any
			full bool
		}{{"2026-09-12", true}, {"2026-09-12T00:00:00Z", false}, {nil, false}} {
			input := Fields{"dueAt": tc.due}
			if action == "create" {
				input["title"] = "洗衣服"
			} else {
				input["todoId"] = "1"
			}
			out, err := Validate(action, input)
			if err != nil || out["isFullDay"] != tc.full {
				t.Fatalf("%s %#v: %#v %v", action, input, out, err)
			}
			if tc.due != nil && !sameNumber(out["dueAt"], int64(1789171200000)) {
				t.Fatalf("wrong timestamp: %#v", out)
			}
		}
	}
	for _, due := range []string{"2026-02-30", "bad-date"} {
		if _, err := Validate("create", Fields{"title": "x", "dueAt": due}); err == nil {
			t.Fatal("accepted invalid date")
		}
	}
	if _, err := Validate("create", Fields{"title": "x", "dueAt": "2026-09-12", "isFullDay": false}); err == nil {
		t.Fatal("overrode explicit false")
	}
	out, err := Validate("update", Fields{"todoId": "1", "title": "x"})
	if err != nil || rawHas(out, "dueAt") || rawHas(out, "isFullDay") {
		t.Fatal("changed omitted update time")
	}
}
