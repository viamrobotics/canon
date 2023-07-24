package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var defaultArgs = []string{"bash", "-l"}

func main() {
	err := parseConfigs()
	if err != nil {
		checkErr(err)
		return
	}

	checkDockerSocket()

	args := flag.Args()
	if len(args) == 0 {
		checkErr(shell(defaultArgs))
	} else {
		switch args[0] {
		case "shell":
			checkErr(shell(defaultArgs))
		case "config":
			showConfig(activeProfile)
		case "update":
			checkErr(checkUpdate(activeProfile, checkAll(args), true))
		case "list":
			checkErr(list())
		case "terminate":
			checkErr(terminate(activeProfile, checkAll(args)))
		case "--":
			fallthrough
		case "run":
			checkErr(shell(args[1:]))
		default:
			checkErr(shell(args))
		}
	}
}

func checkErr(err error) {
	if err == nil {
		return
	}
	_, err2 := fmt.Fprintf(os.Stderr, "Error: %s\n", err)
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
			checkErr(err)
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
