package midi

import (
	"bytes"
	"testing"

	"github.com/mattetti/filebuffer"
)

// transposeFixture is a format 1 MIDI file (96 ticks/quarter) with:
//   - track 0: key signature C major at tick 0, key signature G major at
//     tick 192 (mid-file key change)
//   - track 1: a melody in C major including an F# accidental, a note
//     starting at the exact tick of the key change, and a drum note on
//     channel 10
//
// The F# note_off lands on the same tick as the key change, which exercises
// note_on/note_off pairing across key segments.
var transposeFixture = []byte{
	// header: format 1, 2 tracks, 96 ticks per quarter note
	'M', 'T', 'h', 'd', 0, 0, 0, 6, 0, 1, 0, 2, 0, 96,
	// track 0: conductor track with the key signatures
	'M', 'T', 'r', 'k', 0, 0, 0, 17,
	0x00, 0xFF, 0x59, 0x02, 0x00, 0x00, // tick 0: C major
	0x81, 0x40, 0xFF, 0x59, 0x02, 0x01, 0x00, // tick 192: G major
	0x00, 0xFF, 0x2F, 0x00, // end of track
	// track 1: melody + drums
	'M', 'T', 'r', 'k', 0, 0, 0, 36,
	0x00, 0x90, 60, 100, // tick 0:   C4 on, vel 100
	0x60, 0x80, 60, 64, // tick 96:  C4 off
	0x00, 0x90, 66, 100, // tick 96:  F#4 on (accidental in C major)
	0x60, 0x80, 66, 64, // tick 192: F#4 off (after the key change)
	0x00, 0x90, 64, 90, // tick 192: E4 on (same tick as the key change)
	0x60, 0x80, 64, 64, // tick 288: E4 off
	0x00, 0x99, 38, 100, // tick 288: drum note on channel 10
	0x60, 0x89, 38, 64, // tick 384: drum note off
	0x00, 0xFF, 0x2F, 0x00, // end of track
}

type noteEvent struct {
	msgType  uint8
	channel  uint8
	note     uint8
	velocity uint8
	delta    uint32
	absTicks uint64
}

func collectNoteEvents(t *testing.T, d *Decoder) []noteEvent {
	t.Helper()
	var out []noteEvent
	for _, tr := range d.Tracks {
		for _, ev := range tr.Events {
			if ev.MsgType == EventByteMap["NoteOn"] || ev.MsgType == EventByteMap["NoteOff"] {
				out = append(out, noteEvent{ev.MsgType, ev.MsgChan, ev.Note, ev.Velocity, ev.TimeDelta, ev.AbsTicks})
			}
		}
	}
	return out
}

func decodeFixture(t *testing.T) *Decoder {
	t.Helper()
	d := NewDecoder(bytes.NewReader(append([]byte(nil), transposeFixture...)))
	if err := d.Decode(); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTransposeToKeyFixture(t *testing.T) {
	d := decodeFixture(t)

	// C major -> E major
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}

	// key signature events must be updated: C major -> E major (4 sharps),
	// and the mid-file G major -> B major (5 sharps), same diatonic interval.
	if len(d.Tracks[0].Events) != 3 {
		t.Fatalf("expected 3 events in track 0, got %d", len(d.Tracks[0].Events))
	}
	expKeys := []KeySignature{{Fifths: 4}, {Fifths: 5}}
	for i, exp := range expKeys {
		ev := d.Tracks[0].Events[i]
		if !isKeySignatureEvent(ev) {
			t.Fatalf("event %d is not a key signature event: %#v", i, ev)
		}
		if got := keySignatureFromEvent(ev); got != exp {
			t.Fatalf("key signature %d: expected %+v, got %+v", i, exp, got)
		}
	}

	// Melody: C4->E4 (degree 0), F#4->A#4 (raised degree 3 keeps its
	// accidental offset), E4 in G major -> G#4 in B major (degree 5).
	// The drum note on channel 10 is untouched. Ticks and velocities are
	// preserved.
	noteOn, noteOff := EventByteMap["NoteOn"], EventByteMap["NoteOff"]
	exp := []noteEvent{
		{noteOn, 0, 64, 100, 0x00, 0},
		{noteOff, 0, 64, 64, 0x60, 96},
		{noteOn, 0, 70, 100, 0x00, 96},
		{noteOff, 0, 70, 64, 0x60, 192},
		{noteOn, 0, 68, 90, 0x00, 192},
		{noteOff, 0, 68, 64, 0x60, 288},
		{noteOn, 9, 38, 100, 0x00, 288},
		{noteOff, 9, 38, 64, 0x60, 384},
	}
	got := collectNoteEvents(t, d)
	if len(got) != len(exp) {
		t.Fatalf("expected %d note events, got %d", len(exp), len(got))
	}
	for i := range exp {
		if got[i] != exp[i] {
			t.Fatalf("note event %d: expected %+v, got %+v", i, exp[i], got[i])
		}
	}
}

func TestTransposeToKeyFixtureRoundTrip(t *testing.T) {
	d := decodeFixture(t)
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}

	// re-encode and decode again: the result must be stable and the key
	// signature events must survive the encoder.
	w := filebuffer.New(nil)
	defer w.Close()
	enc := NewEncoder(w, d.Format, d.TicksPerQuarterNote)
	enc.Tracks = d.Tracks
	if err := enc.Write(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	d2 := NewDecoder(w)
	if err := d2.Decode(); err != nil {
		t.Fatal(err)
	}

	exp := collectNoteEvents(t, d)
	got := collectNoteEvents(t, d2)
	if len(got) != len(exp) {
		t.Fatalf("expected %d note events after round trip, got %d", len(exp), len(got))
	}
	for i := range exp {
		if got[i] != exp[i] {
			t.Fatalf("note event %d after round trip: expected %+v, got %+v", i, exp[i], got[i])
		}
	}
	var keys []KeySignature
	forEachEvent(d2.Tracks, func(ev *Event) {
		if isKeySignatureEvent(ev) {
			keys = append(keys, keySignatureFromEvent(ev))
		}
	})
	if len(keys) != 2 || keys[0] != MajorKey(4) || keys[1] != MajorKey(5) {
		t.Fatalf("unexpected key signatures after round trip: %+v", keys)
	}
}

func TestTransposeNote(t *testing.T) {
	cMaj := MajorKey(0)
	eMaj := MajorKey(4)
	aMin := MinorKey(0)
	expectations := []struct {
		note     int
		from, to KeySignature
		exp      int
	}{
		// C major -> E major: diatonic notes keep their scale degree
		{60, cMaj, eMaj, 64}, // C4 -> E4
		{62, cMaj, eMaj, 66}, // D4 -> F#4 (not F4: the scale relation is kept)
		{64, cMaj, eMaj, 68}, // E4 -> G#4
		{65, cMaj, eMaj, 69}, // F4 -> A4
		{67, cMaj, eMaj, 71}, // G4 -> B4
		{72, cMaj, eMaj, 76}, // C5 -> E5
		// accidentals keep their relative offset from the scale degree
		{66, cMaj, eMaj, 70}, // F#4 (raised F) -> A#4 (raised A)
		{61, cMaj, eMaj, 65}, // C#4 (raised C) -> E#4/F4 (raised E)
		// natural minor support
		{57, aMin, cMaj, 48}, // A3 -> C3
		{60, cMaj, aMin, 69}, // C4 -> A4
		{59, aMin, eMaj, 54}, // B3 (degree 1 in A minor) -> F#3 (degree 1 in E major)
		// identity
		{60, cMaj, cMaj, 60},
		// clamping to the MIDI range
		{127, cMaj, eMaj, 127}, // G7 -> would be 131, clamped
		{0, eMaj, cMaj, 0},     // C-2 in E major -> would be -4, clamped
	}
	for _, exp := range expectations {
		if got := TransposeNote(exp.note, exp.from, exp.to); got != exp.exp {
			t.Fatalf("TransposeNote(%d, %+v, %+v) = %d, expected %d", exp.note, exp.from, exp.to, got, exp.exp)
		}
	}
}

func TestTransposeToKeyInvalidTarget(t *testing.T) {
	d := decodeFixture(t)
	for _, fifths := range []int{-8, 8, 100} {
		if err := d.TransposeToKey(KeySignature{Fifths: fifths}); err != ErrInvalidKeySignature {
			t.Fatalf("expected ErrInvalidKeySignature for %d fifths, got %v", fifths, err)
		}
	}
}

func TestTransposeToKeyNoKeySignature(t *testing.T) {
	// a file without any key signature event is treated as C major and no
	// key signature event is injected
	d := &Decoder{Tracks: []*Track{{Events: []*Event{
		{MsgType: EventByteMap["NoteOn"], Note: 60, Velocity: 100},
		{MsgType: EventByteMap["NoteOff"], Note: 60, Velocity: 64},
	}}}}
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Events[0].Note != 64 || d.Tracks[0].Events[1].Note != 64 {
		t.Fatalf("expected C->E transposition, got %d/%d", d.Tracks[0].Events[0].Note, d.Tracks[0].Events[1].Note)
	}
	if len(d.Tracks[0].Events) != 2 {
		t.Fatal("no event should have been added")
	}
}

func TestTransposeToKeyMissingNoteOff(t *testing.T) {
	// a note_on without a matching note_off is transposed and left as is
	d := &Decoder{Tracks: []*Track{{Events: []*Event{
		{MsgType: EventByteMap["NoteOn"], Note: 60, Velocity: 100},
	}}}}
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Events[0].Note != 64 {
		t.Fatalf("expected 64, got %d", d.Tracks[0].Events[0].Note)
	}
}

func TestTransposeToKeyOrphanNoteOff(t *testing.T) {
	// a note_off without a matching note_on is transposed standalone
	d := &Decoder{Tracks: []*Track{{Events: []*Event{
		{MsgType: EventByteMap["NoteOff"], Note: 62, Velocity: 64},
	}}}}
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Events[0].Note != 66 {
		t.Fatalf("expected 66, got %d", d.Tracks[0].Events[0].Note)
	}
}

func TestTransposeToKeyNoteOnVelocityZero(t *testing.T) {
	// note_on with velocity 0 is a note_off and pairs with the note_on
	d := &Decoder{Tracks: []*Track{{Events: []*Event{
		{MsgType: EventByteMap["NoteOn"], Note: 60, Velocity: 100},
		{MsgType: EventByteMap["NoteOn"], Note: 60, Velocity: 0},
	}}}}
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Events[0].Note != 64 || d.Tracks[0].Events[1].Note != 64 {
		t.Fatalf("expected paired notes at 64, got %d/%d",
			d.Tracks[0].Events[0].Note, d.Tracks[0].Events[1].Note)
	}
}

func TestTransposeToKeyClampedPair(t *testing.T) {
	// clamping must not break note_on/note_off pairing
	d := &Decoder{Tracks: []*Track{{Events: []*Event{
		{MsgType: EventByteMap["NoteOn"], Note: 127, Velocity: 100},
		{MsgType: EventByteMap["NoteOff"], Note: 127, Velocity: 64},
	}}}}
	if err := d.TransposeToKey(MajorKey(4)); err != nil {
		t.Fatal(err)
	}
	if d.Tracks[0].Events[0].Note != 127 || d.Tracks[0].Events[1].Note != 127 {
		t.Fatalf("expected clamped pair at 127, got %d/%d",
			d.Tracks[0].Events[0].Note, d.Tracks[0].Events[1].Note)
	}
}

func TestTransposeToKeyWithDrums(t *testing.T) {
	d := decodeFixture(t)
	if err := d.TransposeToKey(MajorKey(4), WithDrums()); err != nil {
		t.Fatal(err)
	}
	events := collectNoteEvents(t, d)
	// the drum note (38, channel 9) is now transposed as well: 38 in C major
	// is degree 1 (D2), which maps to F#2 (42) in E major
	if events[6].note != 42 || events[7].note != 42 {
		t.Fatalf("expected drum note transposed to 42, got %d/%d", events[6].note, events[7].note)
	}
}

func TestKeySignatureFlatKeys(t *testing.T) {
	// flat key signatures are stored as negative (signed) fifths
	if got := MinorKey(0).TonicPC(); got != 9 {
		t.Fatalf("expected A minor tonic 9, got %d", got)
	}
	if got := (KeySignature{Fifths: -4}).TonicPC(); got != 8 {
		t.Fatalf("expected Ab major tonic 8, got %d", got)
	}
	// C major -> Ab major: C4 -> Ab4
	if got := TransposeNote(60, MajorKey(0), KeySignature{Fifths: -4}); got != 68 {
		t.Fatalf("expected 68, got %d", got)
	}
	// round trip through the event representation
	ev := &Event{MsgType: EventByteMap["Meta"], Cmd: MetaByteMap["Key Signature"], Key: -4, Scale: 0}
	data, err := ev.Encode()
	if err != nil {
		t.Fatal(err)
	}
	exp := []byte{0x00, 0xFF, 0x59, 0x02, 0xFC, 0x00}
	if !bytes.Equal(data, exp) {
		t.Fatalf("expected key signature event to encode as %v, got %v", exp, data)
	}
}
