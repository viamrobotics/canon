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
	Default        bool          `mapstructure:"default"         yaml:"default"`
	Image          string        `mapstructure:"image"           yaml:"image"`
	ImageAMD64     string        `mapstructure:"image_amd64"     yaml:"image_amd64"`
	Image386       string        `mapstructure:"image_386"       yaml:"image_386"`
	ImageARM64     string        `mapstructure:"image_arm64"     yaml:"image_arm64"`
	ImageARM       string        `mapstructure:"image_arm"       yaml:"image_arm"`
	ImageARMv6     string        `mapstructure:"image_arm_v6"    yaml:"image_arm_v6"`
	Arch           string        `mapstructure:"arch"            yaml:"arch"`
	MinimumDate    time.Time     `mapstructure:"minimum_date"    yaml:"minimum_date"`
	UpdateInterval time.Duration `mapstructure:"update_interval" yaml:"update_interval"`
	Persistent     bool          `mapstructure:"persistent"      yaml:"persistent"`
	SSH            bool          `mapstructure:"ssh"             yaml:"ssh"`
	NetRC          bool          `mapstructure:"netrc"           yaml:"netrc"`
	User           string        `mapstructure:"user"            yaml:"user"`
	Group          string        `mapstructure:"group"           yaml:"group"`
	Path           string        `mapstructure:"path"            yaml:"path"`
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
		fmt.Fprintf(os.Stderr, "  List active canon-managed container(s)\n  %s list\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  Terminate (stop/close) canon-managed container(s)\n  %s terminate [-a(ll)]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options (defaults shown from current profile):\n")
		flag.PrintDefaults()
	}

	flag.StringVar(&cfgPath, "config", userCfgPath, "config file")
	flag.StringVar(&profileName, "profile", defProfileName, "profile name")
	flag.StringVar(&activeProfile.Image, "image", activeProfile.Image, "docker image name")
	flag.StringVar(&activeProfile.Arch, "arch", activeProfile.Arch, "architecture (\"amd64\", \"arm64\", \"386\", \"arm\", \"arm/v6\")")
	flag.StringVar(&activeProfile.User, "user", activeProfile.User, "user to map to inside the canon environment")
	flag.StringVar(&activeProfile.Group, "group", activeProfile.Group, "group to map to inside the canon environment")
	flag.BoolVar(&activeProfile.SSH, "ssh", activeProfile.SSH, "mount ~/.ssh (read-only) and forward SSH_AUTH_SOCK to the canon environment")
	flag.BoolVar(&activeProfile.NetRC, "netrc", activeProfile.NetRC, "mount ~/.netrc (read-only) in the canon environment")

	flag.Parse()

	// swap again in case a CLI arg would change arch
	swapArchImage(activeProfile)
	return validateArch(activeProfile.Arch)
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
	for _, img := range []string{tempProf.ImageAMD64, tempProf.ImageARM64, tempProf.ImageARM, tempProf.ImageARMv6, tempProf.Image386} {
		if img != "" {
			out.Image = ""
			break
		}
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

	candidates := map[string]bool{}

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
				candidates[pName] = false
				isDef, ok := prof["default"]
				if !ok {
					continue
				}
				defBool, ok := isDef.(bool)
				if !ok {
					continue
				}
				if defBool {
					candidates[pName] = true
				}
			}
		}
		if cwd == string(os.PathSeparator) || len(candidates) > 0 {
			break
		}
		cwd = filepath.Dir(cwd)
	}

	if len(candidates) == 1 {
		for p := range candidates {
			return p, nil
		}
	}

	if len(candidates) > 1 {
		var numDefaults int
		var firstDef string
		for prof, isDef := range candidates {
			if isDef {
				numDefaults++
				if firstDef == "" {
					firstDef = prof
				}
			}
		}
		if numDefaults != 1 {
			keys := []string{}
			for k := range candidates {
				keys = append(keys, k)
			}
			if numDefaults == 0 {
				return "", fmt.Errorf("multiple profiles %s match the current path, and none have the 'default' value set", keys)
			}
			if numDefaults > 1 {
				return "", fmt.Errorf("multiple profiles %s match the current path and have the 'default' value set", keys)
			}
		}
		return firstDef, nil
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
	var canSwap bool
	for _, img := range []string{profile.ImageAMD64, profile.ImageARM64, profile.ImageARM, profile.ImageARMv6, profile.Image386} {
		if profile.Image == "" || img == profile.Image {
			canSwap = true
			break
		}
	}
	if !canSwap {
		return
	}

	switch profile.Arch {
	case "amd64":
		profile.Image = profile.ImageAMD64
	case "arm64":
		profile.Image = profile.ImageARM64
	case "arm":
		profile.Image = profile.ImageARM
	case "arm/v6":
		profile.Image = profile.ImageARMv6
	case "386":
		profile.Image = profile.Image386
	default:
		profile.Image = ""
	}
}

func showConfig(profile *Profile) {
	ret, err := yaml.Marshal(mergedCfg)
	printIfErr(err)
	fmt.Printf("# All explicitly parsed/merged config files (without builtin/default/cli)\n---\n%s\n\n", ret)

	ret, err = yaml.Marshal(map[string]Profile{profile.name: *profile})
	printIfErr(err)
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

func validateArch(arch string) error {
	switch arch {
	case "amd64":
		fallthrough
	case "arm64":
		fallthrough
	case "arm":
		fallthrough
	case "arm/v6":
		fallthrough
	case "386":
		return nil

	case "armv7":
		fallthrough
	case "armv7l":
		fallthrough
	case "armhf":
		fallthrough
	case "arm/v7":
		return errors.New("Invalid architecture: " + arch + "; Use just \"arm\"")

	case "armv6":
		fallthrough
	case "armv6l":
		fallthrough
	case "armel":
		return errors.New("Invalid architecture: " + arch + "; Use \"arm/v6\"")

	case "x86_64":
		return errors.New("Invalid architecture: " + arch + "; Use \"amd64\"")

	case "arm/v8":
		fallthrough
	case "aarch64":
		return errors.New("Invalid architecture: " + arch + "; Use \"arm64\"")

	case "x86":
		fallthrough
	case "i386":
		fallthrough
	case "i686":
		return errors.New("Invalid architecture: " + arch + "; Use \"386\"")

	default:
		return errors.New("Invalid architecture: " + arch)
	}
}
