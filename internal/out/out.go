package out

import (
	"encoding/json"
	"fmt"
	"io"
)

type envelope struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Error   *string     `json:"error"`
}

func WriteJSON(w io.Writer, data interface{}) error {
	b, err := json.Marshal(envelope{Success: true, Data: data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	if isPlatformBrokenPipe(err) {
		return nil
	}
	return err
}

func WriteError(w io.Writer, asJSON bool, err error) error {
	if err == nil {
		return nil
	}
	if asJSON {
		msg := err.Error()
		b, _ := json.Marshal(envelope{Success: false, Data: nil, Error: &msg})
		_, _ = fmt.Fprintln(w, string(b))
		return nil
	}
	_, _ = fmt.Fprintln(w, SanitizeHuman(err.Error()))
	return nil
}
