package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var defaultArgs = []string{"bash", "-l"}

func main() {
	exitCode := 0
	defer func() { os.Exit(exitCode) }()

	err := parseConfigs()
	if err != nil {
		printIfErr(err)
		exitCode = ExitCodeOnError
		return
	}

	checkDockerSocket()

	args := flag.Args()
	if len(args) == 0 {
		exitCode, err = shell(defaultArgs)
		printIfErr(err)
	} else {
		switch args[0] {
		case "shell":
			exitCode, err = shell(defaultArgs)
			printIfErr(err)
		case "config":
			showConfig(activeProfile)
		case "update":
			err = checkUpdate(activeProfile, checkAll(args), true)
			if err != nil {
				exitCode = ExitCodeOnError
				printIfErr(err)
			}
		case "list":
			err = list(context.Background())
			if err != nil {
				exitCode = ExitCodeOnError
				printIfErr(err)
			}
		case "stop":
			err = stop(context.Background(), activeProfile, checkAll(args), false)
			if err != nil {
				exitCode = ExitCodeOnError
				printIfErr(err)
			}
		case "terminate":
			err = stop(context.Background(), activeProfile, checkAll(args), true)
			if err != nil {
				exitCode = ExitCodeOnError
				printIfErr(err)
			}
		case "--":
			fallthrough
		case "run":
			exitCode, err = shell(args[1:])
			printIfErr(err)
		default:
			exitCode, err = shell(args)
			printIfErr(err)
		}
	}
}

func printIfErr(err error) {
	if err == nil {
		return
	}

	pc, filename, line, _ := runtime.Caller(1)
	_, err2 := fmt.Fprintf(os.Stderr, "Error in %s[%s:%d] %v\n", runtime.FuncForPC(pc).Name(), filename, line, err)

	if err2 != nil {
		fmt.Printf("Error encountered printing to stderr: %s\nOriginal Error: %s", err2, err)
	}
}

// On docker Desktop 4.18+ there's a high security option to run without admin permissions.
func checkDockerSocket() {
	_, ok := os.LookupEnv("DOCKER_HOST")
	if !ok {
		_, err := os.Stat("/var/run/docker.sock")
		if err != nil {
			homedir, err := os.UserHomeDir()
			printIfErr(err)
			if err == nil {
				hostPath := filepath.Join(homedir, ".docker/run/docker.sock")
				_, err = os.Stat(hostPath)
				if err == nil {
					os.Setenv("DOCKER_HOST", "unix://"+hostPath)
				}
			}
		}
	}
}
