package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/owncloud/reva/v2/pkg/storage/fs/kiteworks/fixture"
)

func env(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: %s is not set\n", key)
		os.Exit(1)
	}
	return v
}

func mgr() *fixture.Manager {
	insecure := strings.ToLower(os.Getenv("STORAGE_USERS_KITEWORKS_INSECURE")) == "true"
	return fixture.New(env("STORAGE_USERS_KITEWORKS_ENDPOINT"), env("STORAGE_USERS_KITEWORKS_API_TOKEN"), insecure)
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: kw-fixture <command> [args]

commands:
  setup   <name>              create top-level folder; print ID to stdout
  teardown <id>               delete folder by ID
  mkdir   <parent-id> <path>  create nested path; print leaf ID to stdout
  upload  <parent-id> <name> <content>  upload file; print file ID to stdout

env: STORAGE_USERS_KITEWORKS_ENDPOINT, STORAGE_USERS_KITEWORKS_API_TOKEN, STORAGE_USERS_KITEWORKS_INSECURE (optional, default false)`)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "setup":
		if len(os.Args) != 3 {
			usage()
		}
		id, err := mgr().Setup(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(id)

	case "teardown":
		if len(os.Args) != 3 {
			usage()
		}
		m := mgr()
		m.Track(os.Args[2])
		m.Teardown()

	case "mkdir":
		if len(os.Args) != 4 {
			usage()
		}
		id, err := mgr().MkdirAll(os.Args[2], os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(id)

	case "upload":
		if len(os.Args) != 5 {
			usage()
		}
		id, err := mgr().UploadFile(os.Args[2], os.Args[3], []byte(os.Args[4]))
		if err != nil {
			fmt.Fprintf(os.Stderr, "upload: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(id)

	default:
		usage()
	}
}
