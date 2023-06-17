package dealer

type RawMessage struct {
	Type    string            `json:"type"`
	Method  string            `json:"method"`
	Uri     string            `json:"uri"`
	Headers map[string]string `json:"headers"`
}
