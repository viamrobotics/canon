package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"gopkg.in/yaml.v3"
)

const (
	checkDataRelPath = ".cache/canon/update-data.yaml"
	lockRelPath      = ".cache/canon/update.lock"
)

type ImageDef struct {
	Image    string
	Platform string
}

// MarshalYAML marshals yaml
//
//nolint:unparam
func (i ImageDef) MarshalYAML() (interface{}, error) {
	return i.Image + "|" + i.Platform, nil
}

// UnmarshalYAML unmarshals yaml.
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
	lock, err := getLock()
	if err != nil {
		return err
	}
	defer func() {
		checkErr(dropLock(lock))
	}()

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
		if err != nil {
			return err
		}
		defer resp.Close()
		err = jsonmessage.DisplayJSONMessagesStream(resp, os.Stdout, os.Stdout.Fd(), true, nil)
		if err != nil {
			return err
		}
		err = resp.Close()
		if err != nil {
			return err
		}
		checkData[i] = time.Now()
	}
	return checkData.write()
}

func getLock() (*os.File, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	lockFile := filepath.Join(home, lockRelPath)

	if err := os.MkdirAll(filepath.Dir(lockFile), 0o755); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(lockFile, os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(file, "%d", os.Getpid())
	if err != nil {
		return file, err
	}

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EAGAIN) {
		err = errors.New("another canon process is holding " + lockFile)
	}
	return file, err
}

func dropLock(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Remove(file.Name())
}

// Updates the image for the active (default or specified) profile, and (optionally) all known profiles.
func checkUpdate(curProfile *Profile, all bool) error {
	// Used to de-dupe
	imagesMap := make(map[ImageDef]bool)

	lock, err := getLock()
	if err != nil {
		return err
	}
	checkData, err := readCheckData()
	if err != nil {
		return err
	}
	err = dropLock(lock)
	if err != nil {
		return err
	}
	// add current profile's image
	for _, i := range checkImageDate(curProfile, checkData) {
		imagesMap[i] = true
	}

	if all {
		for _, p := range mergedCfg {
			iface, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			prof, err := newProfile(true)
			if err != nil {
				return err
			}

			// we want defaults but NOT the defaults for images
			prof.ImageAMD64 = ""
			prof.ImageARM64 = ""
			prof.Image = ""

			err = mapDecode(iface, prof)
			if err != nil {
				return err
			}

			for _, i := range checkImageDate(prof, checkData) {
				imagesMap[i] = true
			}
		}
	}

	var images []ImageDef
	for i := range imagesMap {
		fmt.Printf("queuing update: %s|%s\n", i.Image, i.Platform)
		images = append(images, i)
	}

	return update(images...)
}

func checkImageDate(profile *Profile, checkData ImageCheckData) []ImageDef {
	var imageCandidates, images []ImageDef

	// Dual arch profile
	switch {
	case profile.ImageAMD64 != "" && profile.ImageARM64 != "":
		imageCandidates = append(imageCandidates, ImageDef{Image: profile.ImageAMD64, Platform: "linux/amd64"})
		imageCandidates = append(imageCandidates, ImageDef{Image: profile.ImageARM64, Platform: "linux/arm64"})
	case profile.Image != "":
		imageCandidates = append(imageCandidates, ImageDef{Image: profile.Image, Platform: profile.Arch})
	default:
		return images
	}

	for _, i := range imageCandidates {
		lastUpdate, ok := checkData[i]
		if !ok || time.Now().After(lastUpdate.Add(profile.UpdateInterval)) || profile.MinimumDate.After(lastUpdate) {
			images = append(images, i)
		}
	}
	return images
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
	if err := os.MkdirAll(filepath.Dir(checkDataFilePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(checkDataFilePath, out, 0o644)
}
