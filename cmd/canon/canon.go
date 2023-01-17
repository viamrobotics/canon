package main

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

	"github.com/moby/term"
)

func main() {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}

	cfg := &container.Config{
		AttachStdin:  true,
		OpenStdin:    true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Image:        "ghcr.io/viamrobotics/canon:amd64",
		Cmd:          []string{"bash"},
	}
	hostCfg := &container.HostConfig{AutoRemove: true}
	netCfg := &network.NetworkingConfig{}

	platform := &v1.Platform{OS: "linux", Architecture: "amd64"}

	name := "canon-temp"

	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, platform, name)
	if err != nil {
		panic(err)
	}

	for _, warn := range resp.Warnings {
		fmt.Printf("Warning during container creation: %s", warn)
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
		panic(err)
	}

	termState, err := term.SetRawTerminal(os.Stdin.Fd())
	if err != nil {
		panic(err)
	}
	defer func() {
		err := term.RestoreTerminal(os.Stdin.Fd(), termState)
		if err != nil {
			panic(err)
		}
	}()

	go func() {
		_, err = io.Copy(os.Stdout, hijack.Reader)
	}()

	go func() {
		_, err = io.Copy(hijack.Conn, os.Stdin)
	}()

	err = cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
	if err != nil {
		panic(err)
	}

	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case <-statusCh:
	}

}
