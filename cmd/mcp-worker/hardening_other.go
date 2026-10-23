//go:build !unix

package main

import "errors"

func disableCoreDumps() error {
	return errors.New("MCP worker requires a supported Unix isolation environment")
}
