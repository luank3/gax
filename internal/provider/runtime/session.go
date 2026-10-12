package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

var (
	processSessionIDOnce sync.Once
	processSessionID     string
)

// ProcessSessionID returns this process's stable request-affinity id, used whenever a request
// belongs to no conversation: recording is off (--no-session, GAX_NO_SESSION), or the caller is
// a metadata/usage request that has no conversation to begin with. The ported behavior resolves the same
// fallback in provider_process_session_id(): the OpenCode gateway rejects requests without a
// session header, so the "{session_id}" placeholder must never expand to an empty value.
func ProcessSessionID() string {
	processSessionIDOnce.Do(func() {
		var raw [16]byte
		// A failed read leaves an all-zero id: still well-formed and non-empty, which is all the
		// session header needs, so there is nothing to report.
		_, _ = rand.Read(raw[:])
		raw[6] = raw[6]&0x0f | 0x40
		raw[8] = raw[8]&0x3f | 0x80
		encoded := hex.EncodeToString(raw[:])
		processSessionID = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	})
	return processSessionID
}
