package console

import (
	"encoding/json"
	"errors"
)

type protectedEnvelope struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	Ciphertext []byte `json:"ciphertext"`
}

func encodeProtectedEnvelope(plain []byte) (json.RawMessage, error) {
	cipher, err := protectUserData(plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protectedEnvelope{1, "dpapi-current-user", cipher})
}

// Callers validate and clear the plaintext, and preserve raw on any failure.
func decodeProtectedEnvelope(raw json.RawMessage) ([]byte, error) {
	var envelope protectedEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != 1 || envelope.Protection != "dpapi-current-user" {
		return nil, errors.New("unsupported protected envelope")
	}
	return unprotectUserData(envelope.Ciphertext)
}
