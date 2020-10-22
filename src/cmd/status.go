package cmd

import (
	"fmt"
	"os"

	"github.com/kevin-hanselman/dud/src/cache"
	"github.com/kevin-hanselman/dud/src/index"
	"github.com/kevin-hanselman/dud/src/stage"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

func printStageStatus(stagePath string, status stage.Status, isLocked bool) error {
	// TODO: use text/tabwriter?
	fmt.Printf("%s  (lock file up-to-date: %t)\n", stagePath, isLocked)
	for path, artStatus := range status {
		if _, err := fmt.Printf("  %s  %s\n", path, artStatus); err != nil {
			return err
		}
	}
	return nil
}

var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"stat", "st"},
	Short:   "Print the status of one or more Dud stages.",
	Long:    "Print the status of one or more Dud stages.",
	Run: func(cmd *cobra.Command, args []string) {

		ch, err := cache.NewLocalCache(viper.GetString("cache"))
		if err != nil {
			logger.Fatal(err)
		}

		idx, err := index.FromFile(".dud/index")
		if os.IsNotExist(err) { // TODO: print error instead?
			idx = make(index.Index)
		} else if err != nil {
			logger.Fatal(err)
		}

		if len(args) == 0 { // By default, check status of everything in the Index.
			for path := range idx {
				args = append(args, path)
			}
		}

		rootDir, err := os.Getwd()
		if err != nil {
			logger.Fatal(err)
		}

		status := make(index.Status)
		for _, path := range args {
			inProgress := make(map[string]bool)
			err := idx.Status(path, ch, rootDir, status, inProgress)
			if err != nil {
				logger.Fatal(err)
			}
		}

		for path, stageStatus := range status {
			if err := printStageStatus(path, stageStatus, idx[path].IsLocked); err != nil {
				logger.Fatal(err)
			}
		}
	},
}
