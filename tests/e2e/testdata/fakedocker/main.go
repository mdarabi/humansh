// This deterministic command exposes Docker's help layout and records argv.
// The installed Humansh and interactive shell are real; no daemon, image pull,
// or request to the reported localhost endpoint is needed by the regression.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	log, err := os.OpenFile(path+".calls.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(os.Args[1:]); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}

	switch strings.Join(os.Args[1:], "\x00") {
	case "--help":
		fmt.Println(`Usage: docker [OPTIONS] COMMAND

Common Commands:
  run  Create and run a new container from an image

Global Options:
  --help  Print usage`)
	case "run\x00--help":
		// These rows retain the installed CLI's lowercase custom value type.
		fmt.Println(`Usage: docker run [OPTIONS] IMAGE [COMMAND] [ARG...]

Create and run a new container from an image

Options:
  --network network  Connect a container to a network
  --rm               Automatically remove the container when it exits
  --help             Print usage`)
	case "run\x00--rm\x00--network\x00host\x00docker.io/library/node@sha256:6dac556d980b7f0e5498d08f08cee0ca67798b4ad6c23964a9214920e67758d0\x00curl\x00--silent\x00--show-error\x00--fail\x00--max-time\x008\x00http://127.0.0.1:3000/api/v1/version":
		fmt.Println("HUMANSH_E2E_DOCKER_EXECUTED")
	default:
		fmt.Fprintf(os.Stderr, "unexpected Docker fixture invocation: %q\n", os.Args[1:])
		os.Exit(97)
	}
}
