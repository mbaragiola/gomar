// Package posbarcode defines the same exact code contract for the app and MCP.
package posbarcode

const MaxLength = 80

// Valid reports whether value is a nonempty sequence of printable ASCII bytes
// other than spaces. A code is never trimmed, case folded, or parsed as a number.
func Valid(value string) bool {
	if len(value) == 0 || len(value) > MaxLength {
		return false
	}
	for _, b := range []byte(value) {
		if b < '!' || b > '~' {
			return false
		}
	}
	return true
}
