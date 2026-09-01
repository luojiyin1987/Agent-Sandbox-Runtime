package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const minimumWriteConfinementABI = 3

type probeResult struct {
	Available        bool   `json:"available"`
	ABI              int    `json:"abi,omitempty"`
	WriteConfinement bool   `json:"write_confinement"`
	ProcessWideTSYNC bool   `json:"process_wide_tsync"`
	Reason           string `json:"reason,omitempty"`
}

type demoResult struct {
	Probe                     probeResult `json:"probe"`
	Enforcement               string      `json:"enforcement"`
	AllowedWriteSucceeded     bool        `json:"allowed_write_succeeded"`
	DeniedCreateBlocked       bool        `json:"denied_create_blocked"`
	DeniedTruncateBlocked     bool        `json:"denied_truncate_blocked"`
	DeniedPathReadSucceeded   bool        `json:"denied_path_read_succeeded"`
}

func main() {
	command := "probe"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	var (
		value any
		err   error
	)

	switch command {
	case "probe":
		value = probePlatform()
	case "demo":
		value, err = demoPlatform()
	case "__child":
		value, err = runChildPlatform()
	default:
		err = fmt.Errorf("unknown command %q (use probe or demo)", command)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
