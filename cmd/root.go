package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name             string
	Image            string
	Arch             string
	Persistent       bool
	Ssh              bool
	Netrc            bool
	User             string
	Group            string
	Path             string
	UpdateInterval   time.Duration
	UpdatePersistent bool
}

var activeProfile = &Profile{
	Name:             "default",
	Image:            "ghcr.io/viamrobotics/canon:latest",
	Arch:             runtime.GOARCH,
	Persistent:       false,
	Ssh:              true,
	Netrc:            true,
	User:             "testbot",
	Group:            "testbot",
	Path:             "/",
	UpdateInterval:   time.Hour * 24,
	UpdatePersistent: true,
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "canon",
	Short: "A tool for running dev environments using docker containers.",
	Long:  "A tool for running dev environments using docker containers. It will mount the current directory inside the docker " +
	"along with (optionally) an SSH agent socket and .netrc file. To do this, it remaps the UID/GID of a given user and group " +
	"within the docker image to match that of the external (normal) user.",
	SilenceUsage: true,
	Args: cobra.ArbitraryArgs,
}

// Global so it can be referenced in update
var mergedCfg map[string]interface{}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	// load a local/project specific config if found
	cfg := make(map[string]interface{})
	repoCfgFile, err := findProjectConfig()
	if err == nil {
		cfg, err = mergeInConfig(cfg, repoCfgFile, true)
		cobra.CheckErr(err)
	}

	// override with settings from user's default or cli specified config
	home, err := os.UserHomeDir()
	cobra.CheckErr(err)

	userCfgPath := home + "/.config/canon.yaml"
	cfgPath := userCfgPath

	if cfgArg := getEarlyFlag("config"); cfgArg != "" {
		cfgPath = cfgArg
	}
	cfg, err = mergeInConfig(cfg, cfgPath, false)
	cobra.CheckErr(err)
	mergedCfg = cfg

	// determine the default profile from configs
	defProfileName, err := getDefaultProfile(cfg)
	cobra.CheckErr(err)
	profileName := defProfileName

	// override with cli specified profile
	if profArg := getEarlyFlag("profile"); profArg != "" {
		profileName = profArg
	}
	// find and load profile
	p, ok := cfg[profileName]
	if !ok {
		cobra.CheckErr(fmt.Errorf("no profile named %s", profileName))
	}
	mapstructure.Decode(p, activeProfile)
	activeProfile.Name = profileName

	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", userCfgPath, "config file")
	rootCmd.PersistentFlags().StringVar(&profileName, "profile", defProfileName, "profile name")
	rootCmd.PersistentFlags().StringVar(&activeProfile.Image, "image", activeProfile.Image, "docker image name")
	rootCmd.PersistentFlags().StringVar(&activeProfile.Arch, "arch", activeProfile.Arch, "architecture (amd64 or arm64)")
	rootCmd.PersistentFlags().StringVar(&activeProfile.User, "user", activeProfile.User, "user to map to inside the canon environment")
	rootCmd.PersistentFlags().StringVar(&activeProfile.Group, "group", activeProfile.Group, "group to map to inside the canon environment")
	rootCmd.PersistentFlags().BoolVar(&activeProfile.Ssh, "ssh", activeProfile.Ssh, "mount ~/.ssh (read-only) and forward SSH_AUTH_SOCK to the canon environment")
	rootCmd.PersistentFlags().BoolVar(&activeProfile.Netrc, "netrc", activeProfile.Netrc, "mount ~/.netrc (read-only) in the canon environment")

	// default to shell subcommand if no subcommand given
	cmd, _, err := rootCmd.Find(os.Args[1:])
	if err == nil && cmd.Use == rootCmd.Use && cmd.Flags().Parse(os.Args[1:]) != pflag.ErrHelp {
		args := append([]string{shellCmd.Use}, os.Args[1:]...)
		rootCmd.SetArgs(args)
	}

	rootCmd.Execute()
}

func findProjectConfig() (path string, err error) {
	var cwd string
	cwd, err = os.Getwd()
	if err != nil {
		return
	}

	for {
		path = filepath.Join(cwd, "canon.yaml")
		_, err = os.Stat(path)
		if err == nil || cwd == string(os.PathSeparator) {
			return
		}

		if errors.Is(err, fs.ErrNotExist) {
			cwd = filepath.Dir(cwd)
		} else {
			return
		}
	}
}

func mergeInConfig(cfg map[string]interface{}, path string, setPath bool) (map[string]interface{}, error) {
	cfgNew := make(map[string]interface{})
	cfgData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(cfgData, cfgNew)
	if err != nil {
		return nil, err
	}

	if setPath {
		for _, v := range cfgNew {
			prof, ok := v.(map[string]interface{})
			if ok {
				_, ok = prof["path"]
				if !ok {
					prof["path"] = filepath.Dir(path)
				}
			}
		}
	}

	outCfg := mergeMaps(cfg, cfgNew)
	return outCfg, nil

}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v, ok := v.(map[string]interface{}); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]interface{}); ok {
					out[k] = mergeMaps(bv, v)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

func getDefaultProfile(cfg map[string]interface{}) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	wd, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}

	for pName, p := range cfg {
		prof, ok := p.(map[string]interface{})
		if !ok || prof == nil {
			continue
		}
		path, ok := prof["path"]
		if !ok {
			continue
		}
		pathStr, ok := path.(string)
		if !ok {
			continue
		}

		hostpath, err := filepath.Abs(pathStr)
		if err != nil {
			return "", err
		}

		for {
			if hostpath == wd {
				return pName, nil
			}
			if hostpath == string(os.PathSeparator) {
				break
			}
			wd = filepath.Dir(wd)
		}
	}

	d, ok := cfg["default_profile"]
	if ok {
		pName, ok := d.(string)
		if ok {
			return pName, nil
		}
	}

	return "", nil
}

func getEarlyFlag(flagName string) string {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--"+flagName) {
			if len(os.Args) >= i {
				return os.Args[i+1]
			}
		}
	}
	return ""
}
