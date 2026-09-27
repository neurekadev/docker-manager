package protocol

// Sequence and gap semantics of event and fs_invalidation frames
// (docs/internal/protocol/agent-v1.md, "Sequence numbers and gaps").
//
//   - The agent numbers event frames and fs_invalidation frames with two
//     independent counters that start at 1 on every session and increase by
//     one per frame.
//   - An agent that cannot send a frame (its bounded relay queue is full)
//     drops it but still consumes its number, so the manager sees a gap.
//   - The manager tracks each counter with a SeqTracker: a number at or
//     below the last one seen is a duplicate (dropped); a number above
//     last+1 is a gap. After a gap (and after every reconnect) the manager
//     stops trusting the event history: it re-reads inventory for Docker
//     events and invalidates (rescans) whole file scopes for fs
//     invalidations, then continues with the new numbers.

// SeqResult classifies an observed sequence number.
type SeqResult int

// Sequence observations.
const (
	// SeqNext is the expected next number.
	SeqNext SeqResult = iota
	// SeqDuplicate was seen already (or is 0); drop the frame.
	SeqDuplicate
	// SeqGap skipped numbers; the frame is still valid, but the receiver
	// must resynchronize what the skipped frames would have told it.
	SeqGap
)

// SeqTracker follows one per-session sequence. The zero value expects 1.
// It is not safe for concurrent use.
type SeqTracker struct {
	last uint64
}

// Observe records seq and classifies it; missed is the number of skipped
// frames for SeqGap.
func (t *SeqTracker) Observe(seq uint64) (res SeqResult, missed uint64) {
	switch {
	case seq == 0 || seq <= t.last:
		return SeqDuplicate, 0
	case seq == t.last+1:
		t.last = seq
		return SeqNext, 0
	}
	missed = seq - t.last - 1
	t.last = seq
	return SeqGap, missed
}

// Last returns the highest number observed.
func (t *SeqTracker) Last() uint64 { return t.last }
