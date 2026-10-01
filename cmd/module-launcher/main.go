package main

import (
	"github.com/housefold/runtime/internal/module"
	"os"
)

func main() {
	if module.LaunchEntry() != nil {
		os.Exit(111)
	}
}
