package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/devxdh/shipwright/pkg/oci"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect [image-url]",
	Short: "Inspect an OCI image manifeset and display metadata",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		imageURI := args[0]
		rawOutput, _ := cmd.Flags().GetBool("raw")

		fmt.Printf("[System Info] Fetching manifest for: %s...\n", imageURI)

		client := oci.NewClient()
		manifest, err := client.FetchManifest(imageURI)
		if err != nil {
			fmt.Printf("[Error] Failed to inspect image: %v\n", err)
			os.Exit(1)
		}

		if rawOutput {
			jsonBytes, _ := json.MarshalIndent(manifest, "", "")
			fmt.Println(string(jsonBytes))
			return
		}

		fmt.Println("\n--- OCI Manifest Summary ---")
		fmt.Printf("Schema Version : %d\n", manifest.SchemaVersion)
		fmt.Printf("Media Type     : %s\n", manifest.MediaType)
		fmt.Printf("Config Digest  : %s\n", manifest.Config.Digest)
		fmt.Printf("Config Size    : %d bytes\n", manifest.Config.Size)
		fmt.Printf("Total Layers   : %d\n", len(manifest.Layers))

		fmt.Println("\n--- Layer Blobs (Top to Bottom) ---")
		var totalSize int64
		for i, layer := range manifest.Layers {
			fmt.Printf("[%02d] %s (%d bytes)\n", i+1, layer.Digest, layer.Size)
			totalSize += layer.Size
		}
		fmt.Printf("\nTotal Compressed Size: %.2f MB\n", float64(totalSize)/(1024*1024))
	},
}

func init() {
	rootCmd.AddCommand(inspectCmd)
	inspectCmd.Flags().Bool("raw", false, "Output raw JSON manifest instead of formated summary")
}
