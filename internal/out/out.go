package out

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
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
	if isBrokenPipe(err) {
		return nil
	}
	return err
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if isPlatformBrokenPipe(err) {
		return true
	}
	var fsPathErr *fs.PathError
	if errors.As(err, &fsPathErr) && isPlatformBrokenPipe(fsPathErr.Err) {
		return true
	}
	var osPathErr *os.PathError
	if errors.As(err, &osPathErr) && isPlatformBrokenPipe(osPathErr.Err) {
		return true
	}
	return false
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
