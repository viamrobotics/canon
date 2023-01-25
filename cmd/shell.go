package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/multierr"

	"github.com/moby/term"
	"github.com/spf13/cobra"
)

// shellCmd represents the shell command
var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Execute a shell in the canon environment (default.)",
	Long: `Exectute a shell in the canon environment.
	This is executed by default if no other command is given.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return shell()
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// shellCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// shellCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
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
	hostCfg := &container.HostConfig{AutoRemove: true}
	netCfg := &network.NetworkingConfig{}

	platform := &v1.Platform{OS: "linux", Architecture: "amd64"}

	name := "canon-temp"

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
	if err != nil {
		return err
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
