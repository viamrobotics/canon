package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/multierr"
	"gopkg.in/yaml.v3"
)

//go:embed canon_setup.sh
var canonSetupScript string

var canonMountPoint = "/host"

func stopContainer(ctx context.Context, cli *client.Client, containerID string) error {
	err := cli.ContainerStop(ctx, containerID, container.StopOptions{})
	if err != nil {
		return err
	}

	// wait for the container to exit
	statusCh, errCh := cli.ContainerWait(ctx, containerID, container.WaitConditionRemoved)
	select {
	case err := <-errCh:
		if err != nil {
			if !strings.Contains(err.Error(), "No such container") {
				return err
			}
		}
	case status := <-statusCh:
		if status.Error != nil && status.Error.Message != "" {
			return fmt.Errorf("error waiting for container stop: %s", status.Error.Message)
		}
	}
	return nil
}

func startContainer(ctx context.Context, cli *client.Client, profile *Profile, sshSock string) (string, error) {
	cfg := &container.Config{
		Image:        profile.Image,
		AttachStdout: true,
		AttachStderr: true,
	}

	hostCfg := &container.HostConfig{AutoRemove: !profile.Persistent}
	netCfg := &network.NetworkingConfig{}
	platform := &v1.Platform{OS: "linux", Architecture: profile.Arch}
	if profile.SSH {
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

		_, err = os.Stat(userSSHDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}

		if err == nil {
			// TODO check inside the container for the actual home directory
			canonSSHDir := "/home/" + profile.User + "/.ssh"

			mnt := mount.Mount{
				Type:     "bind",
				Source:   userSSHDir,
				Target:   canonSSHDir,
				ReadOnly: true,
			}
			hostCfg.Mounts = append(hostCfg.Mounts, mnt)
			cfg.Env = []string{"CANON_SSH=true"}
		}
	}

	if profile.NetRC {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		userNetRC := filepath.Join(home, ".netrc")

		_, err = os.Stat(userNetRC)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}

		if err == nil {
			canonNetRC := "/home/" + profile.User + "/.netrc"
			mnt := mount.Mount{
				Type:     "bind",
				Source:   userNetRC,
				Target:   canonNetRC,
				ReadOnly: true,
			}
			hostCfg.Mounts = append(hostCfg.Mounts, mnt)
		}
	}

	if profile.Path == string(os.PathSeparator) {
		if runtime.GOOS == "darwin" {
			return "", errors.New("no profile found that contains the current directory, " +
				"and the root fs (/) cannot be directly mounted on MacOS")
		}

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

	// label the image with the running profile data
	profYaml, err := yaml.Marshal(profile)
	if err != nil {
		return "", err
	}

	cfg.Labels = map[string]string{
		"com.viam.canon.type":         "one-shot",
		"com.viam.canon.profile":      profile.name + "/" + profile.Arch,
		"com.viam.canon.profile-data": string(profYaml),
	}
	if profile.Persistent {
		cfg.Labels["com.viam.canon.type"] = "persistent"
	}

	rando := rand.New(rand.NewSource(time.Now().UnixNano()))
	name := fmt.Sprintf("canon-%s-%x", profile.name, rando.Uint32())

	// fill out the entrypoint template
	canonSetupScript = strings.ReplaceAll(canonSetupScript, "__CANON_USER__", profile.User)
	canonSetupScript = strings.ReplaceAll(canonSetupScript, "__CANON_GROUP__", profile.Group)
	canonSetupScript = strings.ReplaceAll(canonSetupScript, "__CANON_UID__", fmt.Sprint(os.Getuid()))
	canonSetupScript = strings.ReplaceAll(canonSetupScript, "__CANON_GID__", fmt.Sprint(os.Getgid()))
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

	fmt.Printf("Started new container: %s\n", name)
	containerID := resp.ID

	hijack, err := cli.ContainerAttach(ctx, containerID, types.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return "", err
	}
	defer hijack.Close()

	err = cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
	if err != nil {
		return containerID, err
	}

	// docker attach multiplexes stdout and stderr with a custom byte stream, we have to de-multiplex to strip the extra bytes
	pipeR, pipeW := io.Pipe()
	bufR := bufio.NewReader(pipeR)
	go func() {
		_, err := stdcopy.StdCopy(pipeW, pipeW, hijack.Reader)
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			checkErr(err)
		}
	}()

	scanner := bufio.NewScanner(bufR)
	for scanner.Scan() {
		output := scanner.Text()
		fmt.Println(output)
		if strings.Contains(output, "CANON_READY") {
			break
		}
	}
	return containerID, scanner.Err()
}

func terminate(profile *Profile, all bool) error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	f := filters.NewArgs()
	if all {
		f.Add("label", "com.viam.canon.profile")
	} else {
		f.Add("label", "com.viam.canon.profile="+profile.name+"/"+profile.Arch)
	}
	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{Filters: f})
	if err != nil {
		return err
	}
	if len(containers) > 1 && !all {
		return errors.New("multiple matching containers found, please retry with '--all' option")
	}
	for _, c := range containers {
		fmt.Printf("terminating %s\n", c.Labels["com.viam.canon.profile"])
		err = multierr.Combine(
			err,
			cli.ContainerStop(context.Background(), c.ID, container.StopOptions{}),
			cli.ContainerRemove(context.Background(), c.ID, container.RemoveOptions{}),
		)
	}
	return err
}

func list() error {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	f := filters.NewArgs(filters.Arg("label", "com.viam.canon.profile"))

	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{Filters: f})
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		fmt.Println("No running canon containers found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 0, '\t', 0)
	fmt.Fprintln(w, "Profile/Architecture\tImage\tContainerID")
	fmt.Fprintln(w, "--------------------\t-----\t-----------")
	for _, c := range containers {
		fmt.Fprintf(w, "%s\t%s\t%s\n", c.Labels["com.viam.canon.profile"], c.Image, c.ID)
	}
	return w.Flush()
}

func getPersistentContainer(ctx context.Context, cli *client.Client, profile *Profile) (string, error) {
	f := filters.NewArgs()
	f.Add("label", "com.viam.canon.type=persistent")
	f.Add("label", "com.viam.canon.profile="+profile.name+"/"+profile.Arch)
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{Filters: f})
	if err != nil {
		return "", err
	}
	if len(containers) > 1 {
		return "", fmt.Errorf("more than one container is running for profile %s, please terminate all containers and retry", profile.name)
	}
	if len(containers) < 1 {
		return "", nil
	}

	curProfYaml, err := yaml.Marshal(profile)
	if err != nil {
		return "", err
	}

	profYaml, ok := containers[0].Labels["com.viam.canon.profile-data"]
	if !ok {
		return "", fmt.Errorf("no profile data on persistent container for %s, please terminate all containers and retry", profile.name)
	}

	if profYaml != string(curProfYaml) {
		return "", fmt.Errorf(
			"existing container settings for %s don't match current settings, please terminate all containers and retry",
			profile.name,
		)
	}

	return containers[0].ID, nil
}

func checkContainerImageVersion(ctx context.Context, cli *client.Client, containerID string) (bool, error) {
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}

	containerImageID := info.Image
	imageName := info.Config.Image
	imageInfo, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return false, err
	}

	if imageInfo.ID == containerImageID {
		return false, nil
	}
	return true, nil
}
