package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/multierr"

	"github.com/moby/term"
)

//go:embed canon_setup.sh
var canonSetupScript string

var canonMountPoint = "/host"

func shell(args []string) (err error) {
	if len(args) < 1 {
		return errors.New("shell needs at least one argument to run")
	}
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	var sshSock string
	if activeProfile.Ssh {
		if runtime.GOOS == "darwin" {
			// Docker has magic paths for this on Mac
			sshSock = "/run/host-services/ssh-auth.sock"
		} else {
			sshSock, _ = os.LookupEnv("SSH_AUTH_SOCK")
		}
	}

	containerID, err := startContainer(ctx, cli, activeProfile, sshSock)
	if err != nil {
		return err
	}

	wd, err := getWorkingDir(activeProfile)
	if err != nil {
		return err
	}

	execCfg := types.ExecConfig{
		User:         fmt.Sprintf("%s:%s", activeProfile.User, activeProfile.Group),
		WorkingDir:   wd,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          args,
	}
	if sshSock != "" {
		execCfg.Env = []string{"SSH_AUTH_SOCK="+sshSock}
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	execID := execResp.ID

	hijack, err := cli.ContainerExecAttach(ctx, execID, types.ExecStartCheck{Tty: execCfg.Tty})
	if err != nil {
		return err
	}
	defer hijack.Close()

	//keep the TTY the same size in the container as on the host
	err = resizeTty(ctx, cli, execID)
	if err != nil {
		// for very fast commands, the resize may happen too early or too late
		if !strings.Contains(err.Error(), "cannot resize a stopped container") &&
		   !strings.Contains(err.Error(), "no such exec") {
			return err
		}
	}
	monitorTtySize(ctx, cli, execID)

	termState, err := term.SetRawTerminal(os.Stdin.Fd())
	if err != nil {
		return err
	}
	defer func() {
		err = multierr.Combine(err, term.RestoreTerminal(os.Stdin.Fd(), termState))
	}()

	outErr := make(chan (error))
	inErr := make(chan (error))
	go func() {
		_, err := io.Copy(os.Stdout, hijack.Reader)
		outErr <- err
	}()
	go func() {
		_, err := io.Copy(hijack.Conn, os.Stdin)
		inErr <- err
	}()

	err = cli.ContainerExecStart(ctx, execID, types.ExecStartCheck{})
	if err != nil {
		return err
	}

	select {
	case err := <-outErr:
		if err != nil {
			return err
		}
		break
	case err := <-inErr:
		if err != nil {
			return err
		}
		select {
		case err := <-outErr:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if !activeProfile.Persistent {
		return stopContainer(ctx, cli, containerID)
	}
	return nil
}

func resizeTty(ctx context.Context, cli *client.Client, execID string) error {
	termSize, err := term.GetWinsize(os.Stdout.Fd())
	if err != nil {
		return err
	}
	resizeOpts := types.ResizeOptions{
		Height: uint(termSize.Height),
		Width:  uint(termSize.Width),
	}
	return cli.ContainerExecResize(ctx, execID, resizeOpts)
}

func monitorTtySize(ctx context.Context, cli *client.Client, execID string) {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGWINCH)
	go func() {
		for range sigchan {
			// error is ignored, as there's no way to salvage it if it occurs, and we DO want it to try again on the next resize
			resizeTty(ctx, cli, execID)
		}
	}()
}

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
	swapArchImage(profile)
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
	name := "canon-"+profile.Name
	if profile.Ssh {
		if sshSock != "" {
				mnt := mount.Mount{
				Type:     "bind",
				Source:   sshSock,
				Target:   sshSock,
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
		checkErr(err)
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
		Type:     "bind",
		Source:   profile.Path,
		Target:   canonMountPoint,
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
			err2 := update(imageDef{image: cfg.Image, platform: platform.OS + "/" + platform.Architecture})
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

func getWorkingDir(profile *Profile) (string, error) {
	// start in the right workdir
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(cwd, profile.Path) {
		return "", fmt.Errorf("current directory is not within the current profile's path")
	}
	cwd = strings.TrimPrefix(cwd, profile.Path)
	return filepath.Join(canonMountPoint, cwd), nil
}
