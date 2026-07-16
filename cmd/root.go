/*
Package cmd handles CLI

Copyright © 2026 @devxdh
*/
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "shipwright",
	Short: "An OCI container image replication and transport engine",
	Long: `Shipwright is a stateless, concurrent OCI artifact replication tool.
It fetches manifests, verifies SHA-256 cryptographic digests, and transports
container image layers across registries without requiring a Docker daemon.`,
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("registry", "r", "docker.io", "Default OCI registry hostname")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Enable verbose debug logging")
}
