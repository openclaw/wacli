package main

import (
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newDocsCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "docs",
		Short: "Print documentation URL",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags != nil && flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]string{"url": docsURL})
			}
			_, err := fmt.Fprintln(os.Stdout, docsURL)
			return err
		},
	}
}
