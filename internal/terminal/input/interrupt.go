package input

import "time"

const escapeSequenceTimeout = 50 * time.Millisecond

type escapeState uint8

const (
	escapeIdle escapeState = iota
	escapePending
	escapeCSI
	escapeSS3
)

// escapeClassifier distinguishes a bare Escape from terminal navigation sequences.
type escapeClassifier struct{ state escapeState }

func (classifier *escapeClassifier) Feed(value byte) int {
	switch classifier.state {
	case escapeIdle:
		if value == 0x1b {
			classifier.state = escapePending
		}
	case escapePending:
		switch value {
		case '[':
			classifier.state = escapeCSI
		case 'O':
			classifier.state = escapeSS3
		default:
			if value == 0x1b {
				classifier.state = escapePending
			} else {
				classifier.state = escapeIdle
			}
			return 1
		}
	case escapeCSI:
		if (value >= 0x40 && value <= 0x7e) || value < 0x20 || value > 0x7e {
			classifier.state = escapeIdle
		}
	case escapeSS3:
		classifier.state = escapeIdle
	}
	return 0
}

func (classifier *escapeClassifier) Pending() bool { return classifier.state == escapePending }

func (classifier *escapeClassifier) Timeout() bool {
	if !classifier.Pending() {
		return false
	}
	classifier.state = escapeIdle
	return true
}

func pollTimeoutMillis(timeout time.Duration) int {
	if timeout <= 0 {
		return 0
	}
	return int((timeout + time.Millisecond - 1) / time.Millisecond)
}
