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
	name           string
	Image          string        `yaml:"image" mapstructure:"image"`
	ImageAMD64     string        `yaml:"image_amd64" mapstructure:"image_amd64"`
	ImageARM64     string        `yaml:"image_arm64" mapstructure:"image_arm64"`
	Arch           string        `yaml:"arch" mapstructure:"arch"`
	MinimumDate    time.Time     `yaml:"minimum_date" mapstructure:"minimum_date"`
	UpdateInterval time.Duration `yaml:"update_interval" mapstructure:"update_interval"`
	Persistent     bool          `yaml:"persistent" mapstructure:"persistent"`
	SSH            bool          `yaml:"ssh" mapstructure:"ssh"`
	NetRC          bool          `yaml:"netrc" mapstructure:"netrc"`
	User           string        `yaml:"user" mapstructure:"user"`
	Group          string        `yaml:"group" mapstructure:"group"`
	Path           string        `yaml:"path" mapstructure:"path"`
}

var activeProfile = &Profile{}

func newProfile(loadUserDefaults bool) (*Profile, error) {
	prof := &Profile{
		name:           "builtin",
		Image:          "debian:latest",
		Arch:           runtime.GOARCH,
		MinimumDate:    time.Time{},
		UpdateInterval: time.Hour * 24,
		Persistent:     false,
		SSH:            true,
		NetRC:          true,
		User:           "canon",
		Group:          "canon",
		Path:           "/",
	}

	if loadUserDefaults {
		def, ok := mergedCfg["defaults"]
		if ok {
			return prof, mergeProfile(def, prof)
		}
	}
	return prof, nil
}

// Global so it can be referenced in update.
var mergedCfg map[string]interface{}

func parseConfigs() error {
	// load a local/project specific config if found
	cfg := make(map[string]interface{})
	projCfgFile, err := findProjectConfig()
	if err == nil {
		cfg, err = mergeInConfig(cfg, projCfgFile, true)
		if err != nil {
			return err
		}
	}
	// override with settings from user's default or cli specified config
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userCfgPath := home + "/.config/canon.yaml"
	cfgPath := userCfgPath

	if cfgArg := getEarlyFlag("config"); cfgArg != "" {
		cfgPath = cfgArg
	}
	cfg, err = mergeInConfig(cfg, cfgPath, false)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	mergedCfg = cfg

	activeProfile, err = newProfile(true)
	if err != nil {
		return err
	}

	// determine the default profile from configs
	defProfileName, err := getDefaultProfile(cfg)
	if err != nil {
		return err
	}
	profileName := defProfileName

	// override with cli specified profile
	if profArg := getEarlyFlag("profile"); profArg != "" {
		profileName = profArg
	}

	if profileName != "" {
		// find and load profile
		p, ok := cfg[profileName]
		if !ok {
			return fmt.Errorf("no profile named %s", profileName)
		}
		if err := mergeProfile(p, activeProfile); err != nil {
			return err
		}
		activeProfile.name = profileName
	}

	// if arch-specific images are set, use one of those for displaying defaults in help output
	swapArchImage(activeProfile)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n\n")
		fmt.Fprintf(os.Stderr, "  Interactive shell\n  %s [shell]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Directly run a command\n  %s command arg1 ... argN\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Show current config\n  %s config\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Update docker images\n  %s update [-a(ll)]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Terminate (stop/close) canon-managed container(s)\n  %s terminate [-a(ll)]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options (defaults shown from current profile):\n")
		flag.PrintDefaults()
	}

	flag.StringVar(&cfgPath, "config", userCfgPath, "config file")
	flag.StringVar(&profileName, "profile", defProfileName, "profile name")
	flag.StringVar(&activeProfile.Image, "image", activeProfile.Image, "docker image name")
	flag.StringVar(&activeProfile.Arch, "arch", activeProfile.Arch, "architecture (\"amd64\" or \"arm64\")")
	flag.StringVar(&activeProfile.User, "user", activeProfile.User, "user to map to inside the canon environment")
	flag.StringVar(&activeProfile.Group, "group", activeProfile.Group, "group to map to inside the canon environment")
	flag.BoolVar(&activeProfile.SSH, "ssh", activeProfile.SSH, "mount ~/.ssh (read-only) and forward SSH_AUTH_SOCK to the canon environment")
	flag.BoolVar(&activeProfile.NetRC, "netrc", activeProfile.NetRC, "mount ~/.netrc (read-only) in the canon environment")

	flag.Parse()
	// swap again in case a CLI arg would change arch
	swapArchImage(activeProfile)
	return nil
}

func findProjectConfig() (string, error) {
	var cwd string
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		path := filepath.Join(cwd, ".canon.yaml")
		_, err = os.Stat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || cwd == string(os.PathSeparator) {
			return "", err
		}
		cwd = filepath.Dir(cwd)
	}
}

func mergeInConfig(cfg map[string]interface{}, path string, setPath bool) (map[string]interface{}, error) {
	cfgNew := make(map[string]interface{})
	cfgData, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	err = yaml.Unmarshal(cfgData, cfgNew)
	if err != nil {
		return nil, err
	}

	for k, v := range cfgNew {
		prof, ok := v.(map[string]interface{})
		if ok {
			_, ok = prof["path"]
			if !ok && setPath {
				prof["path"] = filepath.Dir(path)
			}

			prev, ok := cfg[k]
			if !ok {
				continue
			}
			prevProf, ok := prev.(map[string]interface{})
			if !ok {
				continue
			}

			if _, ok := prof["image"]; ok {
				delete(prevProf, "image_amd64")
				delete(prevProf, "image_arm64")
			}
			_, ok1 := prof["image_amd64"]
			_, ok2 := prof["image_arm64"]
			if ok1 || ok2 {
				delete(prevProf, "image")
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

func mergeProfile(in interface{}, out *Profile) error {
	// decode twice, as it's easier to check against a temp struct
	tempProf := &Profile{}
	if err := mapDecode(in, tempProf); err != nil {
		return err
	}
	if tempProf.ImageAMD64 != "" || tempProf.ImageARM64 != "" {
		out.Image = ""
	}
	return mapDecode(in, out)
}

func getDefaultProfile(cfg map[string]interface{}) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", err
	}

	for {
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
			if hostpath == cwd {
				return pName, nil
			}
		}
		if cwd == string(os.PathSeparator) {
			break
		}
		cwd = filepath.Dir(cwd)
	}

	d, ok := cfg["defaults"]
	if ok {
		dMap, ok := d.(map[string]interface{})
		if ok {
			p, ok := dMap["profile"]
			if ok {
				pName, ok := p.(string)
				if ok {
					return pName, nil
				}
			}
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

func checkAll(args []string) bool {
	all := false
	if len(args) >= 2 {
		if args[1] == "-a" || args[1] == "-all" || args[1] == "--all" {
			all = true
		}
	}
	return all
}

func swapArchImage(profile *Profile) {
	// abort if image is overridden and not one of the swapable options
	if profile.Image != "" && profile.Image != profile.ImageAMD64 && profile.Image != profile.ImageARM64 {
		return
	}

	if profile.Arch == "amd64" && profile.ImageAMD64 != "" {
		profile.Image = profile.ImageAMD64
	}
	if profile.Arch == "arm64" && profile.ImageARM64 != "" {
		profile.Image = profile.ImageARM64
	}
}

func showConfig(profile *Profile) {
	ret, err := yaml.Marshal(mergedCfg)
	checkErr(err)
	fmt.Printf("# All explicitly parsed/merged config files (without builtin/default/cli)\n---\n%s\n\n", ret)

	ret, err = yaml.Marshal(map[string]Profile{profile.name: *profile})
	checkErr(err)
	fmt.Printf("# Active, merged profile (including builtin/user defaults and cli arguments)\n---\n%s\n", ret)
}

func mapDecode(iface interface{}, p *Profile) error {
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.StringToTimeDurationHookFunc(),
		Result:     p,
	})
	if err != nil {
		return err
	}
	return dec.Decode(iface)
}
