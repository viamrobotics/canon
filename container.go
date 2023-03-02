package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/opencontainers/image-spec/specs-go/v1"
)

//go:embed canon_setup.sh
var canonSetupScript string

var canonMountPoint = "/host"

func stopContainer(ctx context.Context, cli *client.Client, containerID string) error {
	stopTimeout := time.Second * 10
	err := cli.ContainerStop(ctx, containerID, &stopTimeout)
	if err != nil {
		return err
	}

	// wait for the container to exit
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionRemoved)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case status := <-statusCh:
		if status.Error != nil && status.Error.Message != "" {
			return fmt.Errorf("Error waiting for container stop: %s\n", status.Error.Message)
		}
	}
	return nil
}

func startContainer(ctx context.Context, cli *client.Client, profile *Profile, sshSock string) (string, error) {
	cfg := &container.Config{
		Image:        profile.Image,
		AttachStdout: true,
	}
	if profile.Ssh {
		cfg.Env = []string{"CANON_SSH=true"}
	}
	hostCfg := &container.HostConfig{AutoRemove: true}
	netCfg := &network.NetworkingConfig{}
	platform := &v1.Platform{OS: "linux", Architecture: profile.Arch}
	name := "canon-" + profile.Name
	if profile.Ssh {
		if sshSock != "" {
			mnt := mount.Mount{
				Type:   "bind",
				Source: sshSock,
				Target: sshSock,
			}
			hostCfg.Mounts = append(hostCfg.Mounts, mnt)
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		userSSHDir := filepath.Join(home, ".ssh")
		// TODO check inside the container for the actual home directory
		canonSSHDir := "/home/" + profile.User + "/.ssh"

		mnt := mount.Mount{
			Type:     "bind",
			Source:   userSSHDir,
			Target:   canonSSHDir,
			ReadOnly: true,
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mnt)
	}

	if profile.Netrc {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		userNetRC := filepath.Join(home, ".netrc")
		canonNetRC := "/home/" + profile.User + "/.netrc"
		mnt := mount.Mount{
			Type:     "bind",
			Source:   userNetRC,
			Target:   canonNetRC,
			ReadOnly: true,
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mnt)
	}

	if profile.Path == string(os.PathSeparator) {
		fmt.Fprintf(os.Stderr,
			"WARNING: profile path is root (%s) so mounting entire host system to %s\n",
			string(os.PathSeparator),
			canonMountPoint,
		)
	}

	mnt := mount.Mount{
		Type:   "bind",
		Source: profile.Path,
		Target: canonMountPoint,
	}
	hostCfg.Mounts = append(hostCfg.Mounts, mnt)

	// fill out the entrypoint template
	canonSetupScript = strings.Replace(canonSetupScript, "__CANON_USER__", profile.User, -1)
	canonSetupScript = strings.Replace(canonSetupScript, "__CANON_GROUP__", profile.Group, -1)
	canonSetupScript = strings.Replace(canonSetupScript, "__CANON_UID__", fmt.Sprint(os.Getuid()), -1)
	canonSetupScript = strings.Replace(canonSetupScript, "__CANON_GID__", fmt.Sprint(os.Getgid()), -1)
	//cfg.Entrypoint = []string{"bash", "-c", canonEntryPoint}
	cfg.Entrypoint = []string{}
	cfg.Cmd = []string{"bash", "-c", canonSetupScript}

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
	if err != nil {
		// if we don't have the image or have the wrong architecture, we have to pull it
		if strings.Contains(err.Error(), "does not match the specified platform") || strings.Contains(err.Error(), "No such image") {
			err2 := update(ImageDef{Image: cfg.Image, Platform: platform.OS + "/" + platform.Architecture})
			if err2 != nil {
				return "", err2
			}
			resp, err = cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}

	for _, warn := range resp.Warnings {
		fmt.Fprintf(os.Stderr, "Warning during container creation: %s\n", warn)
	}

	containerID := resp.ID
	fmt.Printf("Container ID: %s\n", containerID)

	err = cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
	if err != nil {
		return containerID, err
	}

	hijack, err := cli.ContainerAttach(ctx, containerID, types.ContainerAttachOptions{Stream: true, Stdout: true})
	defer hijack.Close()

	scanner := bufio.NewScanner(hijack.Reader)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "CANON_READY") {
			break
		}
	}
	return containerID, scanner.Err()
}

func terminate(profile *Profile, all bool) error {
	return nil
}
