package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect [image-url]",
	Short: "Inspect an OCI image manifeset and display metadata",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		imageURI := args[0]
		fmt.Printf("[System Info] Target image: %s\n", imageURI)
		fmt.Println("[Status] Manifest fetching the engine not yet initalized. Ready for Step 2")
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)

	inspectCmd.Flags().Bool("raw", false, "Output raw JSON manifest instead of formated summary")
}
