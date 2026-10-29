package store

import (
	"testing"
	"time"
)

func TestLoginEventsFiltersBeforeLimit(t *testing.T) {
	f := newIndexedDeleteFixture(t)
	s := f.s
	now := time.Now()
	for _, v := range []struct {
		user    int64
		ip      string
		success bool
	}{{1, "203.0.113.1", false}, {2, "203.0.113.1", false}, {1, "2001:db8::1", true}, {1, "203.0.113.1", true}, {1, "198.51.100.1", false}} {
		if err := s.RecordLoginEvent(v.user, "login", v.ip, v.success, now); err != nil {
			t.Fatal(err)
		}
	}
	failed := false
	success := true
	for _, tc := range []struct {
		user        int64
		ip          string
		result      *bool
		limit, want int
	}{{0, "", nil, 200, 5}, {1, "", nil, 200, 4}, {0, "203.0.113.1", &failed, 1, 1}, {1, "203.0.113.1", &failed, 1, 1}, {0, "2001:db8::1", &success, 1, 1}, {0, "203.0.113.1' OR 1=1 --", nil, 1, 0}, {0, "192.0.2.44", nil, 1, 0}} {
		events, err := s.LoginEventsFiltered(tc.user, tc.limit, tc.ip, tc.result)
		if err != nil || events == nil || len(events) != tc.want {
			t.Fatalf("case %+v => %+v %v", tc, events, err)
		}
		for _, event := range events {
			if tc.user > 0 && event.UserID != tc.user || tc.ip != "" && event.IP != tc.ip || tc.result != nil && event.Success != *tc.result {
				t.Fatalf("predicate escaped %+v", event)
			}
		}
	}
}
