/*
A tool for running dev environments using docker containers. It will mount the current directory inside the docker
along with (optionally) an SSH agent socket and .netrc file. To do this, it remaps the UID/GID of a given user and group
within the docker image to match that of the external (normal) user.
*/
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"gopkg.in/yaml.v3"
)

type Profile struct {
	Name             string
	Image            string
	ImageAMD64       string `yaml:"image_amd64"`
	ImageARM64       string `yaml:"image_arm64"`
	Arch             string
	MinimumDate      time.Time `yaml:"minimum_date"`
	Persistent       bool
	Ssh              bool
	Netrc            bool
	User             string
	Group            string
	Path             string
	UpdateInterval   time.Duration `yaml:"update_interval"`
	UpdatePersistent bool          `yaml:"update_persistent"`
}

var activeProfile = &Profile{
	Name:             "default",
	ImageAMD64:       "ghcr.io/viamrobotics/canon:amd64",
	ImageARM64:       "ghcr.io/viamrobotics/canon:arm64",
	Arch:             runtime.GOARCH,
	MinimumDate:      time.Time{},
	Persistent:       false,
	Ssh:              true,
	Netrc:            true,
	User:             "testbot",
	Group:            "testbot",
	Path:             "/",
	UpdateInterval:   time.Hour * 24,
	UpdatePersistent: true,
}

// Global so it can be referenced in update
var mergedCfg map[string]interface{}

func parseConfigs() {
	// load a local/project specific config if found
	cfg := make(map[string]interface{})
	repoCfgFile, err := findProjectConfig()
	if err == nil {
		cfg, err = mergeInConfig(cfg, repoCfgFile, true)
		checkErr(err)
	}

	// override with settings from user's default or cli specified config
	home, err := os.UserHomeDir()
	checkErr(err)

	userCfgPath := home + "/.config/canon.yaml"
	cfgPath := userCfgPath

	if cfgArg := getEarlyFlag("config"); cfgArg != "" {
		cfgPath = cfgArg
	}
	cfg, err = mergeInConfig(cfg, cfgPath, false)
	checkErr(err)
	mergedCfg = cfg

	// find and load defaults section
	p, ok := cfg["defaults"]
	if ok {
		mapstructure.Decode(p, activeProfile)
	}

	// determine the default profile from configs
	defProfileName, err := getDefaultProfile(cfg)
	checkErr(err)
	profileName := defProfileName

	// override with cli specified profile
	if profArg := getEarlyFlag("profile"); profArg != "" {
		profileName = profArg
	}
	// find and load profile
	p, ok = cfg[profileName]
	if !ok {
		checkErr(fmt.Errorf("no profile named %s", profileName))
	}
	mapstructure.Decode(p, activeProfile)
	activeProfile.Name = profileName

	// if arch-specific images are set, use one of those for defaults
	swapArchImage(activeProfile)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n\n")
		fmt.Fprintf(os.Stderr, "  Interactive shell\n  %s [shell]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Directly run a command\n  %s command arg1 ... argN\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Show current config\n  %s config\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Update docker images\n  %s update [-a(ll)]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options (defaults shown from current profile):\n")
		flag.PrintDefaults()
	}

	flag.StringVar(&cfgPath, "config", userCfgPath, "config file")
	flag.StringVar(&profileName, "profile", defProfileName, "profile name")
	flag.StringVar(&activeProfile.Image, "image", activeProfile.Image, "docker image name")
	flag.StringVar(&activeProfile.Arch, "arch", activeProfile.Arch, "architecture (\"amd64\" or \"arm64\")")
	flag.StringVar(&activeProfile.User, "user", activeProfile.User, "user to map to inside the canon environment")
	flag.StringVar(&activeProfile.Group, "group", activeProfile.Group, "group to map to inside the canon environment")
	flag.BoolVar(&activeProfile.Ssh, "ssh", activeProfile.Ssh, "mount ~/.ssh (read-only) and forward SSH_AUTH_SOCK to the canon environment")
	flag.BoolVar(&activeProfile.Netrc, "netrc", activeProfile.Netrc, "mount ~/.netrc (read-only) in the canon environment")

	flag.Parse()

	var archSwitch bool
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "arch":
			archSwitch = true
		case "image":
			archSwitch = false
		}
	})
	if archSwitch {
		swapArchImage(activeProfile)
	}
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
		if len(os.Args) >= i && (arg == "--"+flagName || arg == "-"+flagName) {
			return os.Args[i+1]
		}
		key, val, ok := strings.Cut(arg, "=")
		if ok && (key == "--"+flagName || key == "-"+flagName) {
			return val
		}
	}
	return ""
}

func swapArchImage(profile *Profile) {
	if profile.Arch == "amd64" && profile.ImageAMD64 != "" {
		profile.Image = profile.ImageAMD64
	}
	if profile.Arch == "arm64" && profile.ImageARM64 != "" {
		profile.Image = profile.ImageARM64
	}
}

func showConfig(profile *Profile) {
	ret, err := yaml.Marshal(profile)
	checkErr(err)
	fmt.Printf("Profile:\n%s\n", ret)
}
