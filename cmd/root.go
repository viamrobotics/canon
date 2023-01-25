package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	HostPath         string
	UpdateInterval   time.Duration
	UpdatePersistent bool
}

var activeProfile = &Profile{
	Name:             "default",
	Image:            "ghcr.io/viamrobotics/canon:amd64",
	Arch:             "amd64",
	Persistent:       false,
	Ssh:              true,
	Netrc:            true,
	User:             "testbot",
	Group:            "testbot",
	HostPath:         "",
	UpdateInterval:   time.Hour * 168,
	UpdatePersistent: true,
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:          "canon",
	Short:        "A tool for running dev environments using docker containers.",
	Long:         `A tool for running dev environements using docker containers.`,
	SilenceUsage: true,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {

	// load a local/project specific config if found
	cfg := make(map[string]interface{})
	repoCfgFile, err := findRepoConfig()
	if err == nil {
		cfg, err = mergeInConfig(cfg, repoCfgFile)
		cobra.CheckErr(err)
	}

	// override with settings from user's default or cli specified config
	home, err := os.UserHomeDir()
	cobra.CheckErr(err)
	userCfgPath := home + "/.config/canon.yaml"
	earlyFlags1 := &pflag.FlagSet{ParseErrorsWhitelist: pflag.ParseErrorsWhitelist{UnknownFlags: true}}
	earlyFlags1.StringVar(&userCfgPath, "config", userCfgPath, "config file")
	cobra.CheckErr(earlyFlags1.Parse(os.Args[1:]))
	cfg, err = mergeInConfig(cfg, userCfgPath)
	cobra.CheckErr(err)

	// determine the default profile from configs
	profileName, err := getDefaultProfile(cfg)
	cobra.CheckErr(err)

	// override with cli specified profile
	earlyFlags2 := &pflag.FlagSet{ParseErrorsWhitelist: pflag.ParseErrorsWhitelist{UnknownFlags: true}}
	earlyFlags2.StringVar(&profileName, "profile", profileName, "profile name")
	cobra.CheckErr(earlyFlags2.Parse(os.Args[1:]))

	// find and load profile
	p, ok := cfg[profileName]
	if !ok {
		cobra.CheckErr(fmt.Errorf("no profile named %s", profileName))
	}
	mapstructure.Decode(p, activeProfile)

	rootCmd.PersistentFlags().SortFlags = true
	rootCmd.PersistentFlags().AddFlagSet(earlyFlags1)
	rootCmd.PersistentFlags().AddFlagSet(earlyFlags2)
	rootCmd.PersistentFlags().StringVar(&activeProfile.Image, "image", activeProfile.Image, "docker image name")
	rootCmd.PersistentFlags().StringVar(&activeProfile.Arch, "arch", activeProfile.Arch, "architecture (amd64 or arm64)")
	rootCmd.PersistentFlags().BoolVar(&activeProfile.Ssh, "ssh", activeProfile.Ssh, "foward ssh config/agent to the canon environment")

	cmd, _, err := rootCmd.Find(os.Args[1:])
	// default cmd if no cmd is given
	if err == nil && cmd.Use == rootCmd.Use && cmd.Flags().Parse(os.Args[1:]) != pflag.ErrHelp {
		args := append([]string{shellCmd.Use}, os.Args[1:]...)
		rootCmd.SetArgs(args)
	}

	cobra.CheckErr(rootCmd.Execute())
}

func findRepoConfig() (path string, err error) {
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
	fmt.Println("MERGING ", path)
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
		path, ok := prof["hostpath"]
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
