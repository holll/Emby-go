package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestScheduledCASCompetingWriters(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	expected, err := s.CreateScheduledTask(ScheduledTask{Name: "Original", Type: "scan", Cron: "0 3 * * *", Params: `{"library_id":0}`})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan struct {
		value ScheduledTask
		err   error
	}, 2)
	for _, name := range []string{"A", "B"} {
		go func(name string) {
			<-start
			next := expected
			next.Name = name
			value, err := s.CompareAndSwapScheduledTask(expected, next)
			results <- struct {
				value ScheduledTask
				err   error
			}{value, err}
		}(name)
	}
	close(start)
	wins, conflicts := 0, 0
	winner := ""
	for range 2 {
		r := <-results
		if r.err == nil {
			wins++
			winner = r.value.Name
		} else if errors.Is(r.err, ErrScheduledConfigChanged) {
			conflicts++
		} else {
			t.Fatal(r.err)
		}
	}
	current, err := s.ScheduledTask(expected.ID)
	if err != nil || wins != 1 || conflicts != 1 || current.Name != winner {
		t.Fatalf("wins=%d conflicts=%d current=%+v err=%v", wins, conflicts, current, err)
	}
	// A toggle performed after the snapshot also invalidates a full-form edit.
	next := current
	next.Name = "stale toggle"
	if err := s.SetScheduledTaskEnabled(current.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSwapScheduledTask(current, next); !errors.Is(err, ErrScheduledConfigChanged) {
		t.Fatalf("toggle conflict=%v", err)
	}
}

func TestScheduledCASIgnoresRunResultsAndAcceptsEqualContentABA(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.CreateScheduledTask(ScheduledTask{Name: "A", Type: "scan", Cron: "0 3 * * *"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScheduledResult(old.ID, "skipped", "fixture"); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Name = "B"
	middle, err := s.CompareAndSwapScheduledTask(old, next)
	if err != nil || middle.LastStatus != "skipped" || middle.LastMessage != "fixture" {
		t.Fatalf("run result: %+v %v", middle, err)
	}
	if _, err := s.CompareAndSwapScheduledTask(middle, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSwapScheduledTask(old, next); err != nil {
		t.Fatalf("equal-content ABA unexpectedly rejected: %v", err)
	}
	current, _ := s.ScheduledTask(old.ID)
	if err := s.DeleteScheduledTask(old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSwapScheduledTask(current, next); !errors.Is(err, ErrScheduledConfigChanged) {
		t.Fatalf("delete race: %v", err)
	}
}
