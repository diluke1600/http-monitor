//go:build !windows

package main

import "context"

func handleWindowsService(run func(context.Context)) bool {
	return false
}
