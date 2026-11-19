package midi

import "errors"

// ErrInvalidKeySignature is returned when a key signature falls outside the
// valid MIDI range of -7 (7 flats) to +7 (7 sharps).
var ErrInvalidKeySignature = errors.New("invalid key signature: fifths must be between -7 and +7")

// KeySignature describes a major or natural minor key. It mirrors the MIDI
// key signature meta event (0x59).
type KeySignature struct {
	// Fifths is the number of sharps (positive) or flats (negative) in the
	// key, from -7 to +7. 0 is C major / A minor.
	Fifths int
	// Minor selects the natural minor scale; false selects the major scale.
	Minor bool
}

// MajorKey returns the major key signature with the given number of fifths
// (e.g. MajorKey(4) is E major).
func MajorKey(fifths int) KeySignature { return KeySignature{Fifths: fifths} }

// MinorKey returns the natural minor key signature with the given number of
// fifths (e.g. MinorKey(0) is A minor).
func MinorKey(fifths int) KeySignature { return KeySignature{Fifths: fifths, Minor: true} }

// Validate reports whether the key signature is representable in a MIDI file.
func (k KeySignature) Validate() error {
	if k.Fifths < -7 || k.Fifths > 7 {
		return ErrInvalidKeySignature
	}
	return nil
}

var (
	majorScale        = [7]int{0, 2, 4, 5, 7, 9, 11}
	naturalMinorScale = [7]int{0, 2, 3, 5, 7, 8, 10}
)

// scale returns the pitch class offsets of the key's scale.
func (k KeySignature) scale() [7]int {
	if k.Minor {
		return naturalMinorScale
	}
	return majorScale
}

// TonicPC returns the pitch class (C=0 ... B=11) of the key's tonic.
// Out-of-range fifths values are normalized modulo 12 so even malformed key
// signature events found in the wild map to a deterministic pitch class.
func (k KeySignature) TonicPC() int {
	pc := posMod(k.Fifths*7, 12)
	if k.Minor {
		pc = posMod(pc+9, 12)
	}
	return pc
}

func posMod(a, b int) int {
	return ((a % b) + b) % b
}

// scaleDegree returns the scale degree (0-6) and the accidental offset in
// semitones of a pitch class within the given scale. Both the major and the
// natural minor scales have gaps of at most 2 semitones, so the accidental
// offset is always 0 (diatonic) or 1 (raised scale degree).
func scaleDegree(scale [7]int, pc int) (degree, accidental int) {
	degree = 0
	for d := 0; d < 7; d++ {
		if scale[d] <= pc {
			degree = d
		}
	}
	return degree, pc - scale[degree]
}

// TransposeNote transposes a single MIDI note number from one key to another
// by scale degree: the note keeps its scale degree and its accidental offset
// relative to the source key, and is rebuilt on the same degree of the target
// key. The octave is anchored to C (pitch class 0). Results are clamped to
// the MIDI note range 0-127.
func TransposeNote(note int, from, to KeySignature) int {
	rel := note - from.TonicPC()
	oct := rel / 12
	if rel < 0 && rel%12 != 0 {
		oct-- // floor division for negative values
	}
	pc := rel - oct*12
	deg, acc := scaleDegree(from.scale(), pc)
	out := to.TonicPC() + oct*12 + to.scale()[deg] + acc
	if out < 0 {
		out = 0
	}
	if out > 127 {
		out = 127
	}
	return out
}

// keyInterval is a diatonic interval: a number of scale steps plus an
// accidental offset in semitones.
type keyInterval struct {
	steps      int
	accidental int
}

// intervalBetween returns the diatonic interval from the tonic of "from" to
// the tonic of "to", measured within the scale of "from".
func intervalBetween(from, to KeySignature) keyInterval {
	rel := posMod(to.TonicPC()-from.TonicPC(), 12)
	deg, acc := scaleDegree(from.scale(), rel)
	return keyInterval{steps: deg, accidental: acc}
}

// shiftKey transposes a key signature by the given diatonic interval,
// measured within the key's own scale. The mode (major/minor) follows the
// target mode when the key shares the root key's mode, and is preserved
// otherwise. The resulting tonic is spelled with the fewest accidentals
// (ties resolve to sharps).
func shiftKey(k KeySignature, iv keyInterval, rootMinor, targetMinor bool) KeySignature {
	minor := k.Minor
	if minor == rootMinor {
		minor = targetMinor
	}
	tonic := posMod(k.TonicPC()+k.scale()[iv.steps]+iv.accidental, 12)
	return keySignatureFor(tonic, minor)
}

// keySignatureFor returns the key signature for a tonic pitch class and mode,
// choosing the spelling with the fewest accidentals (ties resolve to sharps).
func keySignatureFor(tonicPC int, minor bool) KeySignature {
	pc := posMod(tonicPC, 12)
	if minor {
		pc = posMod(pc-9, 12)
	}
	// fifths * 7 ≡ pc (mod 12) and 7*7 ≡ 1 (mod 12), so fifths ≡ 7*pc (mod 12)
	f := (pc * 7) % 12
	if f >= 7 {
		f -= 12
	}
	return KeySignature{Fifths: f, Minor: minor}
}

// TransposeOption customizes the behavior of TransposeToKey.
type TransposeOption func(*transposeConfig)

type transposeConfig struct {
	includeDrums bool
	drumChannel  uint8
}

// WithDrums includes drum channel notes in the transposition. By default
// notes on the drum channel are left untouched.
func WithDrums() TransposeOption {
	return func(c *transposeConfig) { c.includeDrums = true }
}

// WithDrumChannel sets the channel treated as the drum channel
// (zero-based; 9, i.e. MIDI channel 10, is the default).
func WithDrumChannel(ch uint8) TransposeOption {
	return func(c *transposeConfig) { c.drumChannel = ch }
}

// TransposeToKey transposes all melodic notes of the decoded MIDI data
// (format 0 or 1) to the target key using diatonic (scale-degree)
// transposition, and rewrites the key signature meta events accordingly.
// Unlike a fixed semitone shift, this preserves the scale relationships of
// the melody: every note keeps its scale degree and accidental offset.
//
// The transposition interval is derived from the first key signature event
// of the file (C major if the file has none) and the target key, and is
// applied diatonically to every segment:
//   - Notes are interpreted against the key signature in effect at their
//     position, so temporary accidentals keep their relative scale-degree
//     offset and mid-file key changes are each transposed by the same
//     interval within their own scale.
//   - Key signature events are rewritten to the transposed key; the mode of
//     a segment follows the target mode when it matched the root key's mode.
//   - Events are processed in a deterministic order across tracks: absolute
//     ticks, then track index, then event order within the track. A key
//     signature event therefore applies to same-tick events that follow it.
//   - note_on/note_off pairs are rewritten consistently: a note_off (or a
//     note_on with velocity 0) takes the transposed pitch of its matching
//     note_on, even across a key change. Orphan note_offs are transposed
//     standalone; note_ons missing their note_off are simply transposed.
//   - Transposed notes are clamped to the MIDI range 0-127.
//   - Notes on the drum channel (channel 10 by default) are excluded.
//
// Ticks, velocities, channels and the track layout are left untouched.
func (d *Decoder) TransposeToKey(target KeySignature, opts ...TransposeOption) error {
	if err := target.Validate(); err != nil {
		return err
	}
	if d == nil || len(d.Tracks) == 0 {
		return nil
	}
	cfg := transposeConfig{drumChannel: 9}
	for _, o := range opts {
		o(&cfg)
	}

	// First pass: find the root key, i.e. the first key signature event in
	// deterministic event order. Files without a key signature default to
	// C major.
	root := KeySignature{}
	foundRoot := false
	forEachEvent(d.Tracks, func(ev *Event) {
		if foundRoot || !isKeySignatureEvent(ev) {
			return
		}
		root = keySignatureFromEvent(ev)
		foundRoot = true
	})
	iv := intervalBetween(root, target)

	// Second pass: rewrite key signatures and notes.
	current := KeySignature{} // C major until the first key signature event
	currentTarget := shiftKey(current, iv, root.Minor, target.Minor)
	type noteRef struct {
		ch   uint8
		note uint8
	}
	active := map[noteRef][]uint8{}

	noteOn := EventByteMap["NoteOn"]
	noteOff := EventByteMap["NoteOff"]

	forEachEvent(d.Tracks, func(ev *Event) {
		switch {
		case isKeySignatureEvent(ev):
			current = keySignatureFromEvent(ev)
			currentTarget = shiftKey(current, iv, root.Minor, target.Minor)
			ev.Key = int32(currentTarget.Fifths)
			if currentTarget.Minor {
				ev.Scale = 1
			} else {
				ev.Scale = 0
			}
		case ev.MsgType == noteOn && ev.Velocity > 0:
			if !cfg.includeDrums && ev.MsgChan == cfg.drumChannel {
				return
			}
			newNote := uint8(TransposeNote(int(ev.Note), current, currentTarget))
			ref := noteRef{ch: ev.MsgChan, note: ev.Note}
			active[ref] = append(active[ref], newNote)
			ev.Note = newNote
		case ev.MsgType == noteOff || (ev.MsgType == noteOn && ev.Velocity == 0):
			if !cfg.includeDrums && ev.MsgChan == cfg.drumChannel {
				return
			}
			ref := noteRef{ch: ev.MsgChan, note: ev.Note}
			if queue := active[ref]; len(queue) > 0 {
				// Pair with the matching note_on so the pair is rewritten
				// consistently even if the key changed in between.
				ev.Note = queue[0]
				active[ref] = queue[1:]
			} else {
				ev.Note = uint8(TransposeNote(int(ev.Note), current, currentTarget))
			}
		}
	})
	return nil
}

// isKeySignatureEvent reports whether the event is a key signature meta event.
func isKeySignatureEvent(ev *Event) bool {
	return ev.MsgType == EventByteMap["Meta"] && ev.Cmd == MetaByteMap["Key Signature"]
}

// keySignatureFromEvent extracts the key signature from a key signature meta
// event. Any non-zero scale value is treated as minor.
func keySignatureFromEvent(ev *Event) KeySignature {
	return KeySignature{Fifths: int(ev.Key), Minor: ev.Scale != 0}
}

// forEachEvent calls fn for every event across all tracks in a deterministic
// order: absolute ticks, then track index, then event order within the track.
func forEachEvent(tracks []*Track, fn func(ev *Event)) {
	idx := make([]int, len(tracks))
	for {
		best := -1
		var bestTicks uint64
		for ti, tr := range tracks {
			if tr == nil || idx[ti] >= len(tr.Events) {
				continue
			}
			ev := tr.Events[idx[ti]]
			if best == -1 || ev.AbsTicks < bestTicks {
				best = ti
				bestTicks = ev.AbsTicks
			}
		}
		if best == -1 {
			return
		}
		fn(tracks[best].Events[idx[best]])
		idx[best]++
	}
}
