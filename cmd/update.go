package cmd

import (
	"context"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
)

// updateCmd represents the shell command
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update (download) the docker image for the active profile (or all profiles.)",
	Long:  `Updates the image for the active (default or specified) profile, or all known profiles.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := cmd.Flags().GetBool("all")
		cobra.CheckErr(err)
		return runUpdate(all)
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().BoolP("all", "a", false, "Update all profiles.")
}

type imageDef struct {
	image    string
	platform string
}

func update(images ...imageDef) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	for _, i := range images {
		resp, err := cli.ImagePull(ctx, i.image, types.ImagePullOptions{Platform: i.platform})
		cobra.CheckErr(err)
		_, err = io.Copy(os.Stdout, resp)
		cobra.CheckErr(err)
		resp.Close()
	}
	return nil
}

func runUpdate(all bool) error {
	var images []imageDef
	if all {
		for _, p := range mergedCfg {
			var image, platform string
			profile, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			i, ok := profile["image"]
			if !ok {
				continue
			}
			image, ok = i.(string)
			if !ok {
				continue
			}
			a, ok := profile["arch"]
			if ok {
				arch, ok := a.(string)
				if ok {
					platform = "linux/" + arch
				}
			}
			if platform == "" {
				platform = "linux/" + activeProfile.Arch
			}
			images = append(images, imageDef{image: image, platform: platform})
		}
	}
	if len(images) == 0 {
		images = append(images, imageDef{image: activeProfile.Image, platform: "linux/" + activeProfile.Arch})
	}
	return update(images...)
}
