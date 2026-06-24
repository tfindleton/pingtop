package ui

const (
	KeyEscape   = "<escape>"
	KeyUp       = "<up>"
	KeyDown     = "<down>"
	KeyLeft     = "<left>"
	KeyRight    = "<right>"
	KeyPageUp   = "<pageup>"
	KeyPageDown = "<pagedown>"
)

func decodeInputBytes(raw []byte) []string {
	keys := make([]string, 0, len(raw))
	for len(raw) > 0 {
		key, consumed := decodeInputToken(raw)
		if consumed <= 0 {
			break
		}
		if key != "" {
			keys = append(keys, key)
		}
		raw = raw[consumed:]
	}
	return keys
}

func decodeInputToken(raw []byte) (string, int) {
	if len(raw) == 0 {
		return "", 0
	}
	if raw[0] != 0x1b {
		return string(raw[:1]), 1
	}
	if key, consumed, ok := decodeEscapeSequence(raw); ok {
		return key, consumed
	}
	return KeyEscape, 1
}

func decodeEscapeSequence(raw []byte) (string, int, bool) {
	if len(raw) < 2 {
		return "", 0, false
	}
	switch raw[1] {
	case '[':
		return decodeCSISequence(raw)
	case 'O':
		if len(raw) < 3 {
			return "", 0, false
		}
		switch raw[2] {
		case 'A':
			return KeyUp, 3, true
		case 'B':
			return KeyDown, 3, true
		case 'C':
			return KeyRight, 3, true
		case 'D':
			return KeyLeft, 3, true
		}
	}
	return "", 0, false
}

func decodeCSISequence(raw []byte) (string, int, bool) {
	if len(raw) < 3 {
		return "", 0, false
	}
	switch raw[2] {
	case 'A':
		return KeyUp, 3, true
	case 'B':
		return KeyDown, 3, true
	case 'C':
		return KeyRight, 3, true
	case 'D':
		return KeyLeft, 3, true
	case '5':
		if len(raw) >= 4 && raw[3] == '~' {
			return KeyPageUp, 4, true
		}
	case '6':
		if len(raw) >= 4 && raw[3] == '~' {
			return KeyPageDown, 4, true
		}
	}
	return "", 0, false
}

func trailingEscapeSequenceNeedsMore(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	lastEscape := -1
	for index := len(raw) - 1; index >= 0; index-- {
		if raw[index] == 0x1b {
			lastEscape = index
			break
		}
	}
	if lastEscape < 0 {
		return false
	}

	tail := raw[lastEscape:]
	if len(tail) == 1 {
		return true
	}
	switch tail[1] {
	case '[':
		if len(tail) == 2 {
			return true
		}
		switch tail[2] {
		case 'A', 'B', 'C', 'D':
			return false
		case '5', '6':
			return len(tail) < 4 || tail[3] != '~'
		default:
			return false
		}
	case 'O':
		return len(tail) < 3
	default:
		return false
	}
}

func specialWindowsKey(scanCode rune) string {
	switch scanCode {
	case 72:
		return KeyUp
	case 80:
		return KeyDown
	case 75:
		return KeyLeft
	case 77:
		return KeyRight
	case 73:
		return KeyPageUp
	case 81:
		return KeyPageDown
	default:
		return ""
	}
}
