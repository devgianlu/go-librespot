package dealer

type RawMessage struct {
	Type         string            `json:"type"`
	Method       string            `json:"method"`
	Uri          string            `json:"uri"`
	Headers      map[string]string `json:"headers"`
	MessageIdent string            `json:"message_ident"`
	Key          string            `json:"key"`
	Payloads     [][]byte          `json:"payloads"`
	Payload      struct {
		Compressed []byte `json:"compressed"`
	} `json:"payload"`
}

type Reply struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	Payload struct {
		Success bool `json:"success"`
	} `json:"payload"`
}
