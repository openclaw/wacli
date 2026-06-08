//go:build !darwin

package syscontacts

import (
	"context"
	"fmt"
)

func ReadSystem(ctx context.Context) ([]Contact, error) {
	return nil, fmt.Errorf("system contacts import is only supported on macOS; pass --input with JSON/NDJSON contacts to import from a file")
}
