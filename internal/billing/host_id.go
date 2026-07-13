package billing

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var canonicalHostIDPattern = regexp.MustCompile(`(?i)^HOST-[0-9a-f]{16}$`)

// CanonicalHostID converts a billing archive OSI ID into a Dynatrace HOST entity ID.
// Decimal OSI IDs are signed 64-bit values whose bit pattern is the hexadecimal
// suffix of the corresponding HOST entity ID.
func CanonicalHostID(id Identifier) (string, error) {
	raw := strings.TrimSpace(string(id))
	if canonicalHostIDPattern.MatchString(raw) {
		return strings.ToUpper(raw), nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid host OSI ID %q: %w", raw, err)
	}
	return fmt.Sprintf("HOST-%016X", uint64(value)), nil
}
