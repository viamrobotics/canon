# !!! Beta Software !!!
Note, this software is new and currently under beta testing. Use at your own risk.

# Canon

A CLI utility for managing docker-based, canonical development environments. Just run `canon` and you'll be instantly dropped into a shell
in a clean development environent, with all your project/code available. Use it to run complex toolchains without installing locally, and
to avoid the dreaded "But it works on my machine!" when working with other developers.

## How it works

When run, canon creates a docker container using a project or user specified image (containing any/all needed development tools.)
It bind-mounts the project directory into the container at /host and maps an internal user to match the external user's UID and GID
(thus avoiding file permissions issues.) Optionally, it will also forward through an SSH agent and config, as well as .netrc files, so
that priviate git repositories can still be accessed.

## Installation

`go install github.com/viamrobotics/canon@latest`

Make sure your GOBIN is in your PATH. If not, you can add it with something like:
`export PATH="$PATH:~/go/bin"`
Note: This path may vary. See https://go.dev/ref/mod#go-install for details.

Make sure you have Docker installed. If unsure, run `docker version` to verify your system is working.
For Docker install instructions, see https://docs.docker.com/engine/install/

## Usage

Simply run `canon` with no arguments to get a shell inside the default canon environment.

Alternately, you can directly specify a command to be run.
Ex: `canon make tests`

### Arguments

Run `canon -help` for a brief listing of arguments you can set via CLI.

## Configuration

### Example Files

Configuration file examples can be found in the configs directory.

### Configuration Layers

Canon configuration options are grouped into profiles, making it easier to set up custom configurations and switch between them. When run,
multiple layers of configuration are parsed and merged, allowing local/user overrides of repo and default settings. From that, one profile
(the "active profile" is selected, and that is then used to set up the container.

The steps in the configuration parsing are as follows

1. User defaults (if present) are loaded from the `defaults` section of the user config file, overriding built-in default values.
		* User config is at `~/.config/canon.yaml` by default, but can be changed with the `-config` option.
2. Starting from the current directory, the file tree is searched upward for a project level config file named exactly `.canon.yaml`
    * `.canon.yaml` is expected to be at the root/top of any specific project.
    * The `path` setting is set automatically at runtime for all profiles in this config, so that they're tied to the root of the project.
3. All profiles in the user config are then merged, thus allowing user overrides of any project specific settings on a per-profile basis.
4. If `-profile` is specified on the command line, the named profile is loaded. Otherwise, things continue.
5. All loaded profiles with a `path` setting are searched for one that contains the current working directory.
    * This allows profiles to be automatically selected based on the current project/directory.
6. If no matching profile is found, the one named in the `profile` field of the user's `defaults` section is used.
7. If no default is set, the default profile is used (built-in values optionally overridden by the `defaults` section.)

Run `canon config` to see exactly what would be used at any point (and copy it to a profile in your config to modify.) Note that this may
change based on the current project/directory, as well as with different arguments provided to the command.

### Configuration Fields

Profiles are defined with the following fields:

* `arch` The architecture (only amd64 or arm64 supported currently) to run the image as.
	- Note the architecture does NOT have to match the host in most cases where emulation is set up. See [Emulation](#emulation) below
	- Defaults to the detected current architecture.
* `image` The docker image used by this profile. Can be overriden by `-image`
	- Note, this should NOT be defined if using the architecture-specific image options below. It will override them both.
* `image_amd64` The AMD64 specific image to use when that architecture is selected.
* `image_arm64` The ARM64 specific image to use when that architecture is selected.
* `minimum_date` If the created timestamp of the image is older then this, force an update of the image.
	- This allows project maintainers to automatically notified canon (and canon users) when an update is needed for a project.
	- Obtain with `docker inspect -f '{{ .Created }}' IMAGE_NAME`
* `persistent` A boolean that determines if a profile should be run in persistent mode. (See [Persistent Mode](#persistent-mode) below.)
	- Defaults to `false`
* `ssh` A boolean, determining if SSH helpers should be set up. (See SSH below.) Can be overridden with `-ssh`
	- Defaults to `true`
* `netrc` A boolean, determining if the user's .netrc file should be (read-only) mounted to the container. Can be overridden with `-netrc`
	- Defaults to `true`
* `user` The user account (within the image) to enter the container as. Can be overriden by `-user`
	- This user's UID will be changed to match the external user's UID, or created if it does not exist.
	- defaults to `canon`
* `group` The group account (within the image) to enter the container as. Can be overriden by `-group`
	- This group's GID will be changed to match the external user's GID, or created if it does not exist.
	- Defaults to `canon`
* `path` The path to the "root" (top level) folder, which will be mounted at `/host` within the container.
	- This also sets which profile should be auto-selected when running canon in/beneath that location on the host.
	- This should **never** be used within project configs, as the path will be set automatically at runtime for project-based profiles.
* `update_interval` A duration (in Go format) that determines how often to check for updates to an image.
	- Defaults to `24h0m0s`

## Persistent Mode

By default, canon will launch a new docker container, and start a shell inside it, then when the user exits the shell, the docker container
is removed, so the next startup will be another "clean room." This can have drawbacks though, as it prevents multiple shells from being
opened in the same environment, and can make build/download caching inside the container somewhat useless. As an alternate mode, a profile
can be set with the "persistent" value set to true. In this mode, any canon executions that use that profile will be run in the same
container. Exiting a shell (or a command ending) will not terminate the container either.

### Terminating persistent containers

Run: `canon terminate` to terminate the container that would currently be used.
Optionally `-a` can be appended to terminate ALL canon-managed containers.

## Emulation

Docker can be used cross-architecture, such as running arm64 images and toolchains on amd64, and vice versa. This is enabled by default
on the MacOS versions of docker. For Linux, qemu can be installed and configured. Most distros have a package to do this
(look for qemu-user-static), or you can run the following to try it out as a one-shot:

`docker run --rm --privileged multiarch/qemu-user-static --reset -p yes`

Note: This will only last until the next reboot of the Linux system. See https://github.com/multiarch/qemu-user-static for more details.

## Updates

By default, canon will check for an updated image at startup once every update_interval. You can force an update with `canon update` or
you can update all images (that can be found from configs and your current working directory) with `canon update -a`.
If you have trouble, or want to reset the update times, remove the cache file(s) in `~/.cache/canon/`

## Creating Custom Docker Images

Nearly any linux image will work, provided it has a few basic utilities installed.
* bash
* useradd
* usermod
* groupadd
* groupmod
* getent
* chown
* sudo (optional, user will be added to sudoers for password-less root)
* ssh/ssh-add (optional, only needed when using ssh agent forwarding)
* git (optional, sets up a helper in the internal user profile for github redirects through ssh)

If custom toolchains/paths/configs, etc are needed, you should set up a normal user account in the docker configured as needed, then set
the user/group settings in the canon profile to point to it. Then whatever external account you call canon with will be mapped to that user
internally.

# Troubleshooting

A common issue is that during initial startup, it may take 90 seconds or more if the canon user (in the container) owns a LOT of
files. To avoid file permissions uses, the internal user's UID is modified, but this requires modifying the ownership of any files that
belong to that user. If you have an image that contains of lot of data in the user's home directory, this can take a while.

The workaround for this is to enable persistent profiles, so that only the first startup of a container has this delay. Subsequent calls
into the container will be nearly instant afterwards.
