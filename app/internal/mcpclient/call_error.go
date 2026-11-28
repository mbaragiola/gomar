package mcpclient

// callError keeps the existing human explanation visible to MCP clients
// that only show tool errors as text. The suffix is stable across messages.
type callError struct {
	code, message string
}

func (e callError) Error() string {
	return e.message + " [gomar:" + e.code + "]"
}
