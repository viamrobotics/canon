package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/multierr"

	"github.com/moby/term"
	"github.com/spf13/cobra"
)

// shellCmd represents the shell command
var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Execute a shell in the canon environment (default)",
	Long: `Exectute a shell in the canon environment.
	This is executed by default if no other command is given.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return shell()
	},
}

var canonMountPoint = "/host"

func init() {
	rootCmd.AddCommand(shellCmd)
}

func shell() (err error) {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}

	cfg := &container.Config{
		AttachStdin:  true,
		OpenStdin:    true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Image:        activeProfile.Image,
		Cmd:          []string{"bash"},
	}
	hostCfg := &container.HostConfig{}
	netCfg := &network.NetworkingConfig{}
	platform := &v1.Platform{OS: "linux", Architecture: activeProfile.Arch}
	name := "canon-"+activeProfile.Name

	if !activeProfile.Persistent {
		hostCfg.AutoRemove = true
	}

	if activeProfile.Ssh {
		if runtime.GOOS == "darwin" {
			// Docker has magic paths for this on Mac
			darwinSock := "/run/host-services/ssh-auth.sock"
			mnt := mount.Mount{
				Type:     "bind",
				Source:   darwinSock,
				Target:   darwinSock,
			}
			hostCfg.Mounts = append(hostCfg.Mounts, mnt)
			cfg.Env = append(cfg.Env, "SSH_AUTH_SOCK="+darwinSock)
		} else {
			sock, ok := os.LookupEnv("SSH_AUTH_SOCK")
			if ok {
					mnt := mount.Mount{
					Type:     "bind",
					Source:   sock,
					Target:   sock,
				}
				hostCfg.Mounts = append(hostCfg.Mounts, mnt)
				cfg.Env = append(cfg.Env, "SSH_AUTH_SOCK="+sock)
			}
		}


		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		userSSHDir := filepath.Join(home, ".ssh")
		// TODO check inside the container for the actual home directory
		canonSSHDir := "/home/" + activeProfile.User + "/.ssh"

		mnt := mount.Mount{
			Type:     "bind",
			Source:   userSSHDir,
			Target:   canonSSHDir,
			ReadOnly: true,
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mnt)
	}

	if activeProfile.Netrc {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)
		userNetRC := filepath.Join(home, ".netrc")
		canonNetRC := "/home/" + activeProfile.User + "/.netrc"
		mnt := mount.Mount{
			Type:     "bind",
			Source:   userNetRC,
			Target:   canonNetRC,
			ReadOnly: true,
		}
		hostCfg.Mounts = append(hostCfg.Mounts, mnt)
	}

	mnt := mount.Mount{
		Type:     "bind",
		Source:   activeProfile.Path,
		Target:   canonMountPoint,
	}
	hostCfg.Mounts = append(hostCfg.Mounts, mnt)

	// start in the right workdir
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	if !strings.HasPrefix(cwd, activeProfile.Path) {
		return fmt.Errorf("current directory is not within the current profile's path")
	}
	if activeProfile.Path == string(os.PathSeparator) {
		fmt.Fprintf(os.Stderr,
			"WARNING: profile path is root (%s) so mounting entire host system to %s\n",
			string(os.PathSeparator),
			canonMountPoint,
		)
	}else{
		cwd = strings.TrimPrefix(cwd, activeProfile.Path)
	}
	cfg.WorkingDir = canonMountPoint + cwd

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
	if err != nil {
		// if we don't have the image or have the wrong architecture, we have to pull it
		if strings.Contains(err.Error(), "does not match the specified platform") || strings.Contains(err.Error(), "No such image") {
			err2 := update(imageDef{image: cfg.Image, platform: platform.OS + "/" + platform.Architecture})
			if err2 != nil {
				return err2
			}
			resp, err = cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	for _, warn := range resp.Warnings {
		fmt.Fprintf(os.Stderr, "Warning during container creation: %s\n", warn)
	}

	containerID := resp.ID
	fmt.Printf("Container ID: %s\n", containerID)

	options := types.ContainerAttachOptions{
		Stream: true,
		Stdin:  cfg.AttachStdin,
		Stdout: cfg.AttachStdout,
		Stderr: cfg.AttachStderr,
	}

	hijack, err := cli.ContainerAttach(ctx, containerID, options)
	if err != nil {
		return err
	}
	defer hijack.Close()

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

	err = cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
	if err != nil {
		return
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

	// wait for shutdown. won't apply to persistent containers
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case <-statusCh:
	}

	return nil

}
