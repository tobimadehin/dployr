/*
Copyright © 2025 Emmanuel Madehin hello@dployr.dev
*/
package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/tobimadehin/dployr/installer"
)

func main() {
	var installType string

	rootCmd := &cobra.Command{
		Use:   "dployr",
		Short: "your app, your server, your rules",
		Long:  "Deploy and manage your infrastructure with ease. Avoid vendor lock-in.",
		RunE: func(cmd *cobra.Command, args []string) error {
			i := installer.NewInstaller()
			return i.Run(installType)
		},
	}

	rootCmd.Flags().StringVar(&installType, "type", "", "Installation type: docker or standalone")
	rootCmd.MarkFlagRequired("type")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
