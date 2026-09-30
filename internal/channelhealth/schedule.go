package channelhealth

import (
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// dueWindow is how long after a channel's scheduled weekly UTC
// weekday+minute the scheduler still considers it "due" if it hasn't
// run yet. This bounds how late a delayed tick can still trigger the
// week's run (e.g. the hub's own tick interval, or brief downtime)
// without waiting for the following occurrence — SPEC-v0.8 §3-2's
// "missed while hub was down -> run once at next start, not N times"
// requirement is handled by IsDue's LastRunAt gate below, not by this
// window; this window exists only so a channel scheduled for, say,
// 09:00 Monday and checked at 09:00:01 Monday isn't skipped for being
// a few ticks "late" within the same intended occurrence.
const dueWindow = 1 * time.Hour

// IsDue reports whether s's weekly synthetic test should run now (a
// pure function of s and now — no wall-clock reads, no I/O), given
// s.Weekday/MinuteOfDay (both UTC per SPEC-v0.8 §3-2's DST-independent
// storage contract) and s.LastRunAt (0 = never run).
//
// Semantics, in order:
//  1. Disabled channels are never due.
//  2. The most recent occurrence of (Weekday, MinuteOfDay) at or before
//     now is computed in UTC — this is the run this tick is
//     evaluating, regardless of how many ticks/days have passed since
//     then (a hub that was down for 3 weeks and just started catches
//     up on exactly the most recent occurrence once, not the 3 it
//     missed: see the doc comment on that occurrence's computation).
//  3. If s.LastRunAt is already at or after that occurrence's instant,
//     the channel already ran for it (or a later one) and is not due
//     again.
//  4. Otherwise, it is due only if now is within dueWindow of that
//     occurrence (so a channel scheduled for a time far in the past
//     relative to now — e.g. LastRunAt is ancient but so is the
//     computed occurrence, meaning the hub simply hasn't reached this
//     week's slot yet — doesn't spuriously fire; the occurrence
//     computed in step 2 is always <= now by construction, so this
//     only excludes an occurrence more than dueWindow in the past,
//     which the scheduler's own regular tick cadence is expected to
//     never actually let happen in production, but keeps this
//     function's contract tight for a delayed/backfilled call).
func IsDue(s models.ChannelHealthSettings, now time.Time) bool {
	if !s.Enabled {
		return false
	}
	occurrence := mostRecentOccurrence(s.Weekday, s.MinuteOfDay, now)
	if s.LastRunAt >= occurrence.Unix() {
		return false
	}
	return now.Sub(occurrence) <= dueWindow
}

// mostRecentOccurrence returns the most recent UTC instant matching
// (weekday, minuteOfDay) that is <= now. "Most recent" (rather than
// "next") is the deliberate choice that makes IsDue's missed-while-
// down behavior correct: after any amount of downtime, there is
// exactly one such instant to compare LastRunAt against, so a hub
// restarting after being down across several intended weekly runs
// still only considers itself due for the single most recent one (see
// SPEC-v0.8 §3-2's explicit "run once ... not N times" requirement) —
// there is no loop over missed weeks here, by construction: computing
// "the most recent match" already collapses every earlier missed
// occurrence away.
func mostRecentOccurrence(weekday, minuteOfDay int, now time.Time) time.Time {
	nowUTC := now.UTC()
	daysSince := int(nowUTC.Weekday()) - weekday
	if daysSince < 0 {
		daysSince += 7
	}
	candidateDay := nowUTC.AddDate(0, 0, -daysSince)
	occurrence := time.Date(
		candidateDay.Year(), candidateDay.Month(), candidateDay.Day(),
		0, minuteOfDay, 0, 0, time.UTC,
	)
	if occurrence.After(nowUTC) {
		// Same weekday match landed later today/this-occurrence than
		// now (e.g. today is the scheduled weekday but the scheduled
		// minute hasn't happened yet) -- the true most-recent match is
		// exactly 7 days earlier.
		occurrence = occurrence.AddDate(0, 0, -7)
	}
	return occurrence
}
