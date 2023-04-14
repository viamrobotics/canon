package main

import (
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

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/moby/term"
	"go.uber.org/multierr"
)

func shell(args []string) error {
	if len(args) < 1 {
		return errors.New("shell needs at least one argument to run")
	}
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}

	var sshSock string
	if activeProfile.SSH {
		if runtime.GOOS == "darwin" {
			// Docker has magic paths for this on Mac
			sshSock = "/run/host-services/ssh-auth.sock"
		} else {
			sshSock, _ = os.LookupEnv("SSH_AUTH_SOCK")
		}
	}

	checkErr(checkUpdate(activeProfile, false))

	var containerID string
	if activeProfile.Persistent {
		containerID, err = getPersistentContainer(ctx, cli, activeProfile)
		if err != nil {
			return err
		}
	}

	if containerID != "" {
		needsUpdate, err := checkContainerImageVersion(ctx, cli, containerID)
		if err != nil {
			return err
		}
		if needsUpdate {
			fmt.Print(
				"WARNING: Persistent container is using an out of date image.\n" +
					"WARNING: Please terminate and restart to use the new version.\n\n",
			)
		}
	} else {
		containerID, err = startContainer(ctx, cli, activeProfile, sshSock)
		if err != nil {
			return err
		}
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
		execCfg.Env = []string{"SSH_AUTH_SOCK=" + sshSock}
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return err
	}
	execID := execResp.ID

	hijack, err := cli.ContainerExecAttach(ctx, execID, types.ExecStartCheck{Tty: execCfg.Tty})
	if err != nil {
		return err
	}
	defer hijack.Close()

	// keep the TTY the same size in the container as on the host
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
			//nolint:errcheck // no way to salvage error, and we DO want it to try again on the next resize
			resizeTty(ctx, cli, execID)
		}
	}()
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
