package main

// The CAN transport: sending standard (11-bit) frames on the MDB's CAN bus
// and receiving whatever comes back.
//
// Commands go out on Music_Control_ID1/2 (0x4E8/0x4E9) — plain standard-
// frame CAN IDs that app.lisp's proc-sid matches directly, the same
// convention the rest of the vehicle's own control messages use (0x4E0 KERS
// enable, 0x4E2 KERS params, 0x4EB speed limit, ...).
//
// Tunu echoes current playback state back on Music_Status (0x7E6) after
// every command on this channel.

import "fmt"

// Command IDs: Music_Control_ID1/2, matching app.lisp's proc-sid exactly.
const (
	canIDPlayStop = 0x4E8 // byte0: 0x00 stop, N = play song N (matched against whatever's resident)
	canIDVolume   = 0x4E9 // byte0: 0-100, direct percent, no rounding
	canIDStatus   = 0x7E6 // Tunu -> MDB echo: byte0 playing, byte1 song number, byte2 volume%
	canStatusLen  = 3

	// Upload IDs: Music_Upload_Begin/Chunk/End and their status echo,
	// matching app.lisp's handle-upload-begin/chunk/end exactly. Streams a
	// song's decoded MIDI bytes to Tunu immediately before playing it,
	// instead of app.lisp holding every song embedded and resident — see
	// uploadSong in redis.go for the send+retry orchestration.
	canIDUploadBegin   = 0x4EC // songNum, trackSlot, length(LE u16), volume*10, decay*100
	canIDUploadChunk   = 0x4ED // seq, up to 7 raw bytes
	canIDUploadEnd     = 0x4EE // songNum, trackSlot, checksum, reserved
	canIDUploadStatus  = 0x7E7 // Tunu -> MDB echo: songNum, trackSlot, result, reserved
	canUploadStatusLen = 4

	uploadChunkPayload = 7 // bytes of song data per chunk frame (+1 seq byte = 8)

	// maxSongBytes must match app.lisp's song-buf-max exactly — it's the
	// size of the two fixed buffers Tunu allocates once and only ever
	// refills, not recreates. Enforced both here (so `music songs add`
	// fails fast with a clear message) and implicitly by Tunu rejecting an
	// oversized Upload_Begin.
	maxSongBytes = 2048

	// Engine sound: app.lisp's speed/throttle-following engine hum, switched
	// on/off with a volume, echoed back on Engine_Status. See engine.go.
	canIDEngine        = 0x4EA // byte0: 0 off / 1 on, byte1: volume 0-100
	canIDEngineStatus  = 0x7ED // Tunu -> MDB echo: byte0 on, byte1 volume%
	canEngineStatusLen = 2
)

// Upload result codes, matching app.lisp's send-upload-status exactly.
const (
	uploadOK               = 0
	uploadChecksumMismatch = 1
	uploadLengthMismatch   = 2
	uploadRejected         = 3
)

func playFrame(song int) (id uint32, data []byte) {
	return uint32(canIDPlayStop), []byte{byte(song)}
}

func stopFrame() (id uint32, data []byte) {
	return uint32(canIDPlayStop), []byte{0x00}
}

func volumeFrame(pct int) (id uint32, data []byte) {
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	return uint32(canIDVolume), []byte{byte(pct)}
}

func engineFrame(on bool, pct int) (id uint32, data []byte) {
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	b := byte(0)
	if on {
		b = 1
	}
	return uint32(canIDEngine), []byte{b, byte(pct)}
}

func uploadBeginFrame(songNumber, trackSlot, length int, volume, decay float64) (id uint32, data []byte) {
	data = []byte{
		byte(songNumber),
		byte(trackSlot),
		byte(length & 0xFF),
		byte((length >> 8) & 0xFF),
		byte(volume * 10),
		byte(decay * 100),
	}
	return uint32(canIDUploadBegin), data
}

func uploadChunkFrame(seq int, chunk []byte) (id uint32, data []byte) {
	data = make([]byte, 1+len(chunk))
	data[0] = byte(seq)
	copy(data[1:], chunk)
	return uint32(canIDUploadChunk), data
}

func uploadEndFrame(songNumber, trackSlot int, checksum byte) (id uint32, data []byte) {
	return uint32(canIDUploadEnd), []byte{byte(songNumber), byte(trackSlot), checksum, 0}
}

// byteSum matches app.lisp's running checksum exactly: a plain sum-of-bytes,
// wrapping mod 256 — Go's unsigned-byte overflow already wraps the same way,
// so this is just addition, not a lookup-table CRC (deliberately simple,
// since this is transfer-integrity for a low-frequency operation, not
// anything security-sensitive).
func byteSum(b []byte) byte {
	var sum byte
	for _, v := range b {
		sum += v
	}
	return sum
}

// canSink is where CAN frames go: the real bus, or a stand-in for local
// development away from the scooter (log:/null:/can0), so the command
// parsing and Redis plumbing can be exercised on a laptop with no CAN
// hardware at all.
type canSink interface {
	send(id uint32, data []byte) error
	// recv blocks until a frame arrives or the sink is closed, in which case
	// it returns an error. A sink with no real bus (log:, null:) returns
	// errNoRx immediately so the caller can log once and stop polling
	// instead of busy-looping.
	recv() (id uint32, data []byte, err error)
	describe() string
	close()
}

var errNoRx = fmt.Errorf("this device has no receive path")

func openCANSink(spec string) (canSink, error) {
	switch spec {
	case "log:":
		return newLogSink(), nil
	case "null:":
		return newNullSink(), nil
	default:
		return openSocketCAN(spec)
	}
}
