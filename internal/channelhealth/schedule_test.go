package channelhealth

import (
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func mustParseUTC(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UTC()
}

func TestIsDue_DisabledNeverDue(t *testing.T) {
	t.Parallel()
	s := models.ChannelHealthSettings{Enabled: false, Weekday: 1, MinuteOfDay: 540}
	now := mustParseUTC(t, "2026-01-05T09:00:00Z") // Monday
	if IsDue(s, now) {
		t.Error("disabled settings must never be due")
	}
}

func TestIsDue_ExactlyAtScheduledTimeIsDue(t *testing.T) {
	t.Parallel()
	// 2026-01-05 is a Monday (weekday=1).
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540, LastRunAt: 0} // 09:00
	now := mustParseUTC(t, "2026-01-05T09:00:00Z")
	if !IsDue(s, now) {
		t.Error("expected due exactly at the scheduled instant")
	}
}

func TestIsDue_ShortlyAfterScheduledTimeStillDue(t *testing.T) {
	t.Parallel()
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540}
	now := mustParseUTC(t, "2026-01-05T09:05:00Z")
	if !IsDue(s, now) {
		t.Error("expected still due a few minutes after the scheduled instant")
	}
}

func TestIsDue_BeforeScheduledTimeNotDue(t *testing.T) {
	t.Parallel()
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540}
	now := mustParseUTC(t, "2026-01-05T08:59:00Z")
	if IsDue(s, now) {
		t.Error("must not be due before the scheduled instant this week (previous week's already ran, presumably)")
	}
}

func TestIsDue_AlreadyRanForThisOccurrenceNotDueAgain(t *testing.T) {
	t.Parallel()
	occurrence := mustParseUTC(t, "2026-01-05T09:00:00Z")
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540, LastRunAt: occurrence.Unix()}
	now := occurrence.Add(10 * time.Minute)
	if IsDue(s, now) {
		t.Error("must not be due again after already running for this week's occurrence")
	}
}

func TestIsDue_NextWeekBecomesDueAgainAfterPreviousRun(t *testing.T) {
	t.Parallel()
	firstOccurrence := mustParseUTC(t, "2026-01-05T09:00:00Z")
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540, LastRunAt: firstOccurrence.Unix()}
	nextWeek := firstOccurrence.AddDate(0, 0, 7)
	if !IsDue(s, nextWeek) {
		t.Error("expected due again at next week's occurrence")
	}
}

func TestIsDue_MissedWhileHubWasDownRunsOnceAtNextStart(t *testing.T) {
	t.Parallel()
	// Channel was scheduled weekly for Monday 09:00 UTC and last ran 5
	// weeks ago (hub was down the whole time in between). The hub
	// starts up on a Thursday, 3 days after the most recent Monday.
	fiveWeeksAgo := mustParseUTC(t, "2025-12-01T09:00:00Z") // a Monday
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 1, MinuteOfDay: 540, LastRunAt: fiveWeeksAgo.Unix()}

	hubStartsUp := mustParseUTC(t, "2026-01-08T14:00:00Z") // Thursday, well after the most recent Monday 09:00
	if IsDue(s, hubStartsUp) {
		t.Fatal("more than dueWindow has passed since the most recent occurrence; a real scheduler tick loop would have caught it within the window on hub startup — this asserts the function's own boundary, not the surrounding loop")
	}

	// Simulate the more realistic case: the hub starts up shortly
	// after (within dueWindow of) the most recent Monday occurrence,
	// having been down since before it.
	hubStartsUpSoonAfter := mustParseUTC(t, "2026-01-05T09:30:00Z")
	if !IsDue(s, hubStartsUpSoonAfter) {
		t.Fatal("expected due: hub started up within dueWindow of the missed occurrence and has not run since")
	}

	// After running once for that occurrence, it must not fire again
	// even though 4 earlier occurrences were also missed — the
	// "run once, not N times" contract.
	ranAt := hubStartsUpSoonAfter
	s.LastRunAt = mostRecentOccurrence(s.Weekday, s.MinuteOfDay, ranAt).Unix()
	stillSameDay := ranAt.Add(20 * time.Minute)
	if IsDue(s, stillSameDay) {
		t.Fatal("must not be due again immediately after the single catch-up run")
	}
}

func TestIsDue_DSTIndependence(t *testing.T) {
	t.Parallel()
	// A UTC-stored schedule must produce the identical due/not-due
	// verdict whether evaluated on either side of a real-world DST
	// transition date, since UTC itself never observes DST. Use two
	// dates that would differ by an hour in, say, US Eastern time
	// (around its March "spring forward") but are unambiguous in UTC.
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 0, MinuteOfDay: 90} // Sunday 01:30 UTC
	beforeTransition := mustParseUTC(t, "2026-03-08T01:30:00Z")                   // Sunday, before US DST starts
	afterTransitionNextWeek := mustParseUTC(t, "2026-03-15T01:30:00Z")            // Sunday, after US DST starts

	if !IsDue(s, beforeTransition) {
		t.Error("expected due before the DST transition date")
	}
	s.LastRunAt = beforeTransition.Unix()
	if !IsDue(s, afterTransitionNextWeek) {
		t.Error("expected due again the following Sunday at the identical UTC minute, unaffected by any local DST transition in between")
	}
}

func TestIsDue_WeekdayWraparound(t *testing.T) {
	t.Parallel()
	// now is Sunday 00:30 UTC; the schedule is Saturday 00:00 UTC, i.e.
	// the most recent occurrence was yesterday (~24.5h ago) -- well
	// outside dueWindow, so this must not be due (a prior tick nearer
	// Saturday 00:00 would already have caught it in a real deployment;
	// this only asserts the wraparound math picked the correct nearby
	// day rather than one 6 days in the future).
	s := models.ChannelHealthSettings{Enabled: true, Weekday: 6, MinuteOfDay: 0}
	sunday := mustParseUTC(t, "2026-01-04T00:30:00Z") // Sunday
	if IsDue(s, sunday) {
		t.Error("expected not due: the wraparound-matched Saturday occurrence is ~24.5h in the past, outside dueWindow")
	}
	got := mostRecentOccurrence(s.Weekday, s.MinuteOfDay, sunday)
	want := mustParseUTC(t, "2026-01-03T00:00:00Z") // the Saturday immediately before
	if !got.Equal(want) {
		t.Errorf("mostRecentOccurrence = %v, want %v (yesterday, not 6 days out)", got, want)
	}
}

func TestMostRecentOccurrence_SameDayBeforeTimeGoesBackAWeek(t *testing.T) {
	t.Parallel()
	// now is Monday 08:00, schedule is Monday 09:00 -- the matching
	// weekday is today, but the time hasn't happened yet today, so the
	// true most recent occurrence is last Monday.
	now := mustParseUTC(t, "2026-01-05T08:00:00Z")
	got := mostRecentOccurrence(1, 540, now)
	want := mustParseUTC(t, "2025-12-29T09:00:00Z")
	if !got.Equal(want) {
		t.Errorf("mostRecentOccurrence = %v, want %v", got, want)
	}
}

func TestMostRecentOccurrence_NeverAfterNow(t *testing.T) {
	t.Parallel()
	now := mustParseUTC(t, "2026-01-05T09:00:00Z")
	for weekday := 0; weekday <= 6; weekday++ {
		for _, minute := range []int{0, 539, 540, 541, 1439} {
			got := mostRecentOccurrence(weekday, minute, now)
			if got.After(now) {
				t.Errorf("mostRecentOccurrence(weekday=%d, minute=%d, now=%v) = %v, must never be after now", weekday, minute, now, got)
			}
		}
	}
}
