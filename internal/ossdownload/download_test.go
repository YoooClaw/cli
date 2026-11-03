package ossdownload

import "testing"

func TestContentRangeWithoutSizeCap(t *testing.T) {
	const large int64 = 20_000_000_000
	_, _, total, err := parseContentRange("bytes 0-0/20000000000", 0)
	if err != nil || total != large {
		t.Fatalf("large download rejected: total=%d err=%v", total, err)
	}
	if _, _, _, err := parseContentRange("bytes 0-0/20000000000", 1024); err == nil {
		t.Fatal("explicit cap ignored")
	}
	for _, value := range []string{"bytes 1-0/20000000000", "bytes 0-20000000000/20000000000", "bytes 0-0/9223372036854775808"} {
		if _, _, _, err := parseContentRange(value, 0); err == nil {
			t.Fatalf("invalid range accepted: %s", value)
		}
	}
}
