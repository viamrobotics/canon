package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"
)

type ImageDef struct {
	Image          string
	Platform       string
	UpdateInterval time.Duration `yaml:"update_interval,omitempty"`
	LastChecked    time.Time `yaml:"last_checked,omitempty"`
	MinimumDate    time.Time `yaml:"minimum_date,omitempty"`
}

func update(images ...ImageDef) error {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	checkErr(err)
	var prevData []ImageDef
	dataBytes, err := os.ReadFile(filepath.Join(home, ".cache/canon/update-data.yaml"))
	if err != nil && err != os.ErrNotExist{
		return err
	}
	err = yaml.Unmarshal(dataBytes, prevData)
	if err != nil {
		return err
	}

	now := time.Now()
	for _, i := range images {
		needsUpdate := false
		for _, p := range prevData {
			if p.Image == i.Image && p.Platform == i.Platform && now.After(p.LastChecked.Add(i.UpdateInterval)) {
				needsUpdate = true
				break
			}
		}
		if !needsUpdate {
			continue
		}

		resp, err := cli.ImagePull(ctx, i.Image, types.ImagePullOptions{Platform: i.Platform})
		checkErr(err)
		_, err = io.Copy(os.Stdout, resp)
		checkErr(err)
		resp.Close()
	}
	return nil
}

// Updates the image for the active (default or specified) profile, or all known profiles.
func runUpdate(profile *Profile, all bool) error {
	var images []ImageDef
	if all {
		for _, p := range mergedCfg {
			var image, platform string
			prof, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			i, ok := prof["image"]
			if !ok {
				continue
			}
			image, ok = i.(string)
			if !ok {
				continue
			}
			a, ok := prof["arch"]
			if ok {
				arch, ok := a.(string)
				if ok {
					platform = "linux/" + arch
				}
			}
			if platform == "" {
				platform = "linux/" + profile.Arch
			}
			images = append(images, ImageDef{
				Image: image,
				Platform: platform,
				// MinimumDate: minDate,
				// UpdateInterval: updateInterval,
			})
		}
	}
	if len(images) == 0 {
		images = append(images, ImageDef{
			Image: profile.Image,
			Platform: "linux/" + profile.Arch,
			MinimumDate: profile.MinimumDate,
			UpdateInterval: profile.UpdateInterval,
		})
	}
	return update(images...)
}
