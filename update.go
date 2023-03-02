package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/mitchellh/mapstructure"
	"gopkg.in/yaml.v3"
)

const checkDataRelPath = ".cache/canon/update-data.yaml"

type ImageDef struct {
	Image    string
	Platform string
}

func (i ImageDef) MarshalYAML() (interface{}, error) {
	return i.Image + "|" + i.Platform, nil
}

func (i *ImageDef) UnmarshalYAML(n *yaml.Node) error {
	splits := strings.Split(n.Value, "|")
	if len(splits) != 2 {
		return errors.New(n.Value + " did not split into image and platform")
	}
	i.Image = splits[0]
	i.Platform = splits[1]
	return nil
}

type ImageCheckData map[ImageDef]time.Time

func update(images ...ImageDef) error {
	checkData, err := readCheckData()
	if err != nil {
		return err
	}

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	for _, i := range images {
		resp, err := cli.ImagePull(ctx, i.Image, types.ImagePullOptions{Platform: i.Platform})
		checkErr(err)
		_, err = io.Copy(os.Stdout, resp)
		checkErr(err)
		resp.Close()
		checkData[i] = time.Now()
	}
	return checkData.write()
}

// Updates the image for the active (default or specified) profile, and (optionally) all known profiles.
func cmdUpdate(curProfile *Profile, all bool) error {
	// Used to de-dupe
	imagesMap := make(map[ImageDef]bool)
	imagesMap[ImageDef{Image: curProfile.Image, Platform: "linux/" + curProfile.Arch}] = true

	if all {
		for _, p := range mergedCfg {
			iface, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			prof := &Profile{}
			if err := mapstructure.Decode(iface, prof); err != nil {
				return err
			}

			// Dual arch profile
			if prof.ImageAMD64 != "" && prof.ImageARM64 != "" {
				imagesMap[ImageDef{Image: prof.ImageAMD64, Platform: "linux/amd64"}] = true
				imagesMap[ImageDef{Image: prof.ImageARM64, Platform: "linux/arm64"}] = true
				continue
			}

			// No image in this profile
			if prof.Image == "" {
				continue
			}

			// If no arch is specified
			if prof.Arch == "" {
				prof.Arch = curProfile.Arch
			}
			imagesMap[ImageDef{Image: prof.Image, Platform: "linux/" + prof.Arch}] = true
		}
	}

	var images []ImageDef
	for i := range imagesMap {
		images = append(images, i)
	}

	return update(images...)
}

func readCheckData() (ImageCheckData, error) {
	checkData := make(ImageCheckData)
	home, err := os.UserHomeDir()
	if err != nil {
		return checkData, err
	}
	dataBytes, err := os.ReadFile(filepath.Join(home, checkDataRelPath))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return checkData, err
	}
	return checkData, yaml.Unmarshal(dataBytes, checkData)
}

func (data *ImageCheckData) write() error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	checkDataFilePath := filepath.Join(home, checkDataRelPath)
	if err := os.MkdirAll(filepath.Dir(checkDataFilePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(checkDataFilePath, out, 0644)
}
