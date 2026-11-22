package whatsapppreview

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestReplaySyntheticText(t *testing.T) {
	raw, err := os.ReadFile("../../examples/whatsapp-synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Replay(t.Context(), bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	const want = `{"datos_sinteticos":true,"respuesta":"Datos de prueba. Saldos pendientes:\nARS 209,50 pendientes; 128,50 vencidos.\nUSD 6,25 pendientes; 0,00 vencidos."}` + "\n"
	if out.String() != want {
		t.Fatalf("reply = %q", out.String())
	}
}

func TestReplayNoReplyOrInvalid(t *testing.T) {
	raw, err := os.ReadFile("../../examples/whatsapp-synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	base := string(raw)
	cases := []struct {
		name, raw string
		invalid   bool
	}{
		{"unknown sender", strings.Replace(base, "demo-sender", "other-sender", 1), false},
		{"other phrase", strings.Replace(base, "Cuánto debemos", "Otra pregunta", 1), false},
		{"nontext", strings.Replace(base, `"type":"text"`, `"type":"image"`, 1), false},
		{"status", strings.Replace(base, `"messages":[{"from":"demo-sender","id":"wamid.demo-message","type":"text","text":{"body":"Cuánto debemos"}}]`, `"statuses":[{"id":"wamid.demo-message","status":"delivered"}]`, 1), false},
		{"invalid JSON", "{", true},
		{"trailing frame", base + "{}", true},
		{"excess input", base + strings.Repeat(" ", payloadLimit), true},
		{"wrong destination", strings.Replace(base, "demo-destination", "other-destination", 1), true},
		{"missing text", strings.Replace(base, `"text":{"body":"Cuánto debemos"}`, `"text":null`, 1), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Replay(t.Context(), strings.NewReader(c.raw), &out); (err == invalid) != c.invalid || (!c.invalid && err != nil) {
				t.Fatalf("error = %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("unexpected reply")
			}
		})
	}
}
