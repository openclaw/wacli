package app

import (
	"fmt"
	"os"
)

func (a *App) eventsEnabled() bool {
	return a != nil && a.Events().Enabled()
}

func (a *App) emitEvent(event string, data map[string]any) {
	if a == nil {
		return
	}
	_ = a.Events().Emit(event, data)
}

func (a *App) emitOrPrint(event string, data map[string]any, format string, args ...any) {
	if a.eventsEnabled() {
		a.emitEvent(event, data)
		return
	}
	fmt.Fprintf(os.Stderr, format, args...)
}

func (a *App) emitWarning(code, message string, data map[string]any) {
	if a.eventsEnabled() {
		if data == nil {
			data = map[string]any{}
		}
		data["code"] = code
		data["message"] = message
		a.emitEvent("warning", data)
		return
	}
	fmt.Fprintln(os.Stderr, message)
}
