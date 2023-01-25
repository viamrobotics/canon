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
	UpdateInterval:   time.Hour * 168,
	UpdatePersistent: true,
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:          "canon",
	Short:        "A tool for running dev environments using docker containers.",
	Long:         `A tool for running dev environments using docker containers.`,
	SilenceUsage: true,
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
		cfg, err = mergeInConfig(cfg, repoCfgFile)
		cobra.CheckErr(err)
	}

	// override with settings from user's default or cli specified config
	home, err := os.UserHomeDir()
	cobra.CheckErr(err)

	userCfgPath := home + "/.config/canon.yaml"
	cfgPath := userCfgPath

	if cfgArg := getEarlyArg("config"); cfgArg != "" {
		cfgPath = cfgArg
	}
	cfg, err = mergeInConfig(cfg, cfgPath)
	cobra.CheckErr(err)
	mergedCfg = cfg

	// determine the default profile from configs
	defProfileName, err := getDefaultProfile(cfg)
	cobra.CheckErr(err)
	profileName := defProfileName

	// override with cli specified profile
	if profArg := getEarlyArg("profile"); profArg != "" {
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
	rootCmd.PersistentFlags().BoolVar(&activeProfile.Ssh, "ssh", activeProfile.Ssh, "foward ssh config/agent to the canon environment")

	cmd, _, err := rootCmd.Find(os.Args[1:])
	// default cmd if no cmd is given
	if err == nil && cmd.Use == rootCmd.Use && cmd.Flags().Parse(os.Args[1:]) != pflag.ErrHelp {
		args := append([]string{shellCmd.Use}, os.Args[1:]...)
		rootCmd.SetArgs(args)
	}

	rootCmd.Execute()
}

func findProjectConfig() (path string, err error) {
	cwd, err := os.Getwd()
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

func mergeInConfig(cfg map[string]interface{}, path string) (map[string]interface{}, error) {
	cfgNew := make(map[string]interface{})
	cfgData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	//fmt.Println("MERGING ", path)
	err = yaml.Unmarshal(cfgData, cfgNew)
	if err != nil {
		return nil, err
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
			hostpath = filepath.Dir(hostpath)
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

func getEarlyArg(argName string) string {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--"+argName) {
			if len(os.Args) >= i {
				return os.Args[i+1]
			}
		}
	}
	return ""
}
