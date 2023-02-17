package main

import (
	"context"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

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
		checkErr(err)
		_, err = io.Copy(os.Stdout, resp)
		checkErr(err)
		resp.Close()
	}
	return nil
}

// Updates the image for the active (default or specified) profile, or all known profiles.
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
