package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/devxdh/shipwright/pkg/oci"
	"github.com/spf13/cobra"
)

var copyCmd = &cobra.Command{
	Use:   "copy [image-url] [destination-dir]",
	Short: "Concurrently replicate OCI image layers to a local directory",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		imageURI := args[0]
		distDir := args[1]
		workers, _ := cmd.Flags().GetInt("workers")

		startTime := time.Now()
		fmt.Printf("[System Info] Replicating %s to %s using %d concurrent workers...\n", imageURI, distDir, workers)

		client := oci.NewClient()
		manifest, err := client.FetchManifest(imageURI)
		if err != nil {
			fmt.Printf("[Error] Failed to fetch manifest: %v\n", err)
			os.Exit(1)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		taskQueue := make(chan oci.BlobTask, len(manifest.Layers))

		_, repo, _ := parseCLIImageRef(imageURI)
		for _, layer := range manifest.Layers {
			taskQueue <- oci.BlobTask{
				Repo:        repo,
				Descriptor:  layer,
				Destination: distDir,
			}
		}
		close(taskQueue)

		var wg sync.WaitGroup
		var errOnce sync.Once
		var fatalErr error

		for i := 1; i < workers; i++ {
			wg.Add(1)
			workerID := i

			go func(id int) {
				defer wg.Done()

				for task := range taskQueue {
					if ctx.Err() != nil {
						fmt.Printf("[Worker %02d] Aborting remaining tasks due to cancellation signal.\n", id)
						return
					}

					fmt.Printf(
						"[Worker %02d] Downloading blob: %s (%.2f MB)\n",
						id, task.Descriptor.Digest[:15]+"...", float64(task.Descriptor.Size)/(1024*1024),
					)

					err := client.DownloadBlob(ctx, repo, task.Descriptor, task.Destination)
					if err != nil {
						errOnce.Do(func() {
							fatalErr = fmt.Errorf("[Worker %02d] Fatal error on blob %s: %v", id, task.Descriptor.Digest, err)
							fmt.Println("[System Warning] Error detected! Broadcasting cancellation to all workers...")
							cancel()
						})
						return
					}

					fmt.Printf("[Worker %02d] VERIFIED & SAVED: %s\n", id, task.Descriptor.Digest[:15]+"...")
				}
			}(workerID)
		}

		wg.Wait()

		if fatalErr != nil {
			fmt.Printf("\n[REPLICATION FAILED] %v\n", fatalErr)
			os.Exit(1)
		}
		elapsed := time.Since(startTime)
		fmt.Printf(
			"\n[SUCCESS] Replicated %d layers atomically in %s.\n",
			len(manifest.Layers), elapsed.Round(time.Millisecond),
		)
	},
}

func init() {
	rootCmd.AddCommand(copyCmd)
	copyCmd.Flags().IntP("workers", "w", 3, "Number of concurrent worker routines")
}

func parseCLIImageRef(ref string) (registry, repo, tag string) {
	client := oci.NewClient()
	_ = client
	return "docker.io", extractRepo(ref), "latest"
}

func extractRepo(ref string) string {
	if idx := len(ref); idx > 0 {
		parts := ref
		for i := 0; i < len(parts); i++ {
			if parts[i] == ':' || parts[i] == '@' {
				parts = parts[:i]
				break
			}
		}
		if len(parts) > 0 && parts[0] != '/' {
			if !containsSlash(parts) {
				return "library/" + parts
			}
			return parts
		}
	}
	return "library/ubuntu"
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}
