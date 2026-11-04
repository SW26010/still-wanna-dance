package console

import "encoding/json"

// A request rejection is an expected input/admission result. It belongs to the
// current operation, and neither raises a desktop fault nor proves recovery.
type requestRejection struct{ error }

func rejectRequest(err error) error {
	if err == nil {
		return nil
	}
	return &requestRejection{err}
}

func (e *requestRejection) Unwrap() error { return e.error }

func decodeRequest(decoder *json.Decoder, input any) error {
	return rejectRequest(decoder.Decode(input))
}
