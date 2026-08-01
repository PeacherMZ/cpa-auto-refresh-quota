package refreshquota

import (
	"testing"
	"time"
)

func TestNextOccurrenceRequiresLocationAndTimes(t *testing.T) {
	now := time.Date(2026, time.July, 29, 0, 0, 0, 0, time.UTC)
	if got, label, ok := NextOccurrence(now, nil, []DailyTime{{Hour: 8, Text: "08:00:00"}}); ok || !got.IsZero() || label != "" {
		t.Fatalf("NextOccurrence(nil location) = %v, %q, %v; want zero values", got, label, ok)
	}
	if got, label, ok := NextOccurrence(now, time.UTC, nil); ok || !got.IsZero() || label != "" {
		t.Fatalf("NextOccurrence(no times) = %v, %q, %v; want zero values", got, label, ok)
	}
}

func TestNextOccurrenceIsStrictlyAfterNow(t *testing.T) {
	times := []DailyTime{
		{Hour: 8, Minute: 0, Second: 0, Text: "08:00:00"},
		{Hour: 20, Minute: 30, Second: 0, Text: "20:30:00"},
	}
	tests := []struct {
		name      string
		now       time.Time
		want      time.Time
		wantLabel string
	}{
		{
			name:      "before first time",
			now:       time.Date(2026, time.July, 29, 7, 59, 59, 0, time.UTC),
			want:      time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC),
			wantLabel: "08:00:00",
		},
		{
			name:      "exactly at first time advances",
			now:       time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC),
			want:      time.Date(2026, time.July, 29, 20, 30, 0, 0, time.UTC),
			wantLabel: "20:30:00",
		},
		{
			name:      "after final time advances to next day",
			now:       time.Date(2026, time.July, 29, 23, 0, 0, 0, time.UTC),
			want:      time.Date(2026, time.July, 30, 8, 0, 0, 0, time.UTC),
			wantLabel: "08:00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label, ok := NextOccurrence(tt.now, time.UTC, times)
			if !ok {
				t.Fatal("NextOccurrence() ok = false, want true")
			}
			if !got.Equal(tt.want) {
				t.Fatalf("NextOccurrence() = %v, want %v", got, tt.want)
			}
			if label != tt.wantLabel {
				t.Fatalf("label = %q, want %q", label, tt.wantLabel)
			}
			if !got.After(tt.now) {
				t.Fatalf("occurrence %v is not strictly after now %v", got, tt.now)
			}
		})
	}
}

func TestNextOccurrenceDoesNotRequireSortedInput(t *testing.T) {
	now := time.Date(2026, time.July, 29, 7, 0, 0, 0, time.UTC)
	times := []DailyTime{
		{Hour: 20, Minute: 30, Text: "20:30:00"},
		{Hour: 8, Text: "08:00:00"},
		{Hour: 12, Text: "12:00:00"},
	}
	got, label, ok := NextOccurrence(now, time.UTC, times)
	if !ok {
		t.Fatal("NextOccurrence() ok = false, want true")
	}
	want := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) || label != "08:00:00" {
		t.Fatalf("NextOccurrence() = %v, %q; want %v, 08:00:00", got, label, want)
	}
}

func TestNextOccurrenceSkipsNonexistentDSTWallTime(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}

	// America/New_York jumps from 01:59:59 to 03:00:00 on 2024-03-10,
	// so 02:30 does not exist on that civil day.
	now := time.Date(2024, time.March, 10, 0, 30, 0, 0, location)
	times := []DailyTime{{Hour: 2, Minute: 30, Second: 0, Text: "02:30:00"}}
	want := time.Date(2024, time.March, 11, 2, 30, 0, 0, location)

	got, label, ok := NextOccurrence(now, location, times)
	if !ok {
		t.Fatal("NextOccurrence() ok = false, want true")
	}
	if !got.Equal(want) {
		t.Fatalf("NextOccurrence() = %v, want %v", got, want)
	}
	if label != "02:30:00" {
		t.Fatalf("label = %q, want 02:30:00", label)
	}
}

func TestNextOccurrenceUsesOneCanonicalRepeatedDSTOccurrence(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}

	// 01:30 occurs twice on 2024-11-03. time.Date selects one canonical
	// instant for that wall time; the scheduler must not select the other one
	// after the canonical instant has passed.
	item := DailyTime{Hour: 1, Minute: 30, Second: 0, Text: "01:30:00"}
	canonical := time.Date(2024, time.November, 3, 1, 30, 0, 0, location)

	got, label, ok := NextOccurrence(canonical.Add(-time.Minute), location, []DailyTime{item})
	if !ok {
		t.Fatal("NextOccurrence(before repeated time) ok = false, want true")
	}
	if !got.Equal(canonical) {
		t.Fatalf("NextOccurrence(before repeated time) = %v, want canonical %v", got, canonical)
	}
	if label != item.Text {
		t.Fatalf("label = %q, want %q", label, item.Text)
	}

	nextDay := time.Date(2024, time.November, 4, 1, 30, 0, 0, location)
	got, _, ok = NextOccurrence(canonical.Add(time.Minute), location, []DailyTime{item})
	if !ok {
		t.Fatal("NextOccurrence(after canonical repeated time) ok = false, want true")
	}
	if !got.Equal(nextDay) {
		t.Fatalf("NextOccurrence(after canonical repeated time) = %v, want next day %v", got, nextDay)
	}

	firstInstant := time.Date(2024, time.November, 3, 5, 30, 0, 0, time.UTC)
	secondInstant := time.Date(2024, time.November, 3, 6, 30, 0, 0, time.UTC)
	firstID := OccurrenceID(firstInstant, location, item.Text)
	secondID := OccurrenceID(secondInstant, location, item.Text)
	if firstID != secondID {
		t.Fatalf("repeated wall-time IDs differ: first=%q second=%q", firstID, secondID)
	}
}

func TestOccurrenceIDUsesLocalCivilDateLabelAndZone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}

	at := time.Date(2026, time.July, 29, 16, 5, 0, 0, time.UTC)
	got := OccurrenceID(at, location, "00:05:00")
	want := "2026-07-30|00:05:00|Asia/Shanghai"
	if got != want {
		t.Fatalf("OccurrenceID() = %q, want %q", got, want)
	}
}
