// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"os"
	"path/filepath"
)

// OutputDir is the directory where generated videos are stored.
var OutputDir = ""

// EnsureOutputDir creates the video output directory if it doesn't exist.
func EnsureOutputDir(baseDir string) error {
	OutputDir = filepath.Join(baseDir, "videos")
	return os.MkdirAll(OutputDir, 0755)
}
