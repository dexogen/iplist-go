package app

import (
	"testing"
	"time"
)

func TestCronScheduleNext(t *testing.T) {
	schedule, err := parseCronSchedule("0 */12 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(time.Date(2026, 5, 26, 10, 15, 30, 0, time.UTC))
	want := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}

	got = schedule.Next(time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC))
	want = time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next after exact match = %s, want %s", got, want)
	}
}

func TestCronScheduleRejectsInvalidExpression(t *testing.T) {
	if _, err := parseCronSchedule("*/0 * * * *"); err == nil {
		t.Fatal("expected invalid step error")
	}
	if _, err := parseCronSchedule("0 24 * * *"); err == nil {
		t.Fatal("expected hour range error")
	}
	if _, err := parseCronSchedule("0 0 * *"); err == nil {
		t.Fatal("expected field count error")
	}
}
