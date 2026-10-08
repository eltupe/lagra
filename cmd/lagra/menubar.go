package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/eltupe/lagra/internal/app"
	"github.com/eltupe/lagra/internal/menubar"
	"github.com/eltupe/lagra/internal/window"
)

func appCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "app",
		Short: "Run the daemon with the menu bar icon (what the .app bundle and the LaunchAgent start)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New(app.Options{ConfigFlag: configFlag, Version: version, PickDirectory: window.PickDirectory})
			if err != nil {
				return err
			}
			return menubar.Run(context.Background(), a)
		},
	}
}
