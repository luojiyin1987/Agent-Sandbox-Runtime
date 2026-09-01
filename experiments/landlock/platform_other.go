//go:build !linux

package main

import "errors"

func probePlatform() probeResult {
	return probeResult{Reason: "Landlock requires Linux"}
}

func demoPlatform() (demoResult, error) {
	result := demoResult{Probe: probePlatform()}
	return result, errors.New("Landlock experiment requires Linux")
}

func runChildPlatform() (demoResult, error) {
	result := demoResult{Probe: probePlatform()}
	return result, errors.New("Landlock experiment requires Linux")
}
