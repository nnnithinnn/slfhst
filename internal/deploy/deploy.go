// Package deploy is stage2's image-pull + stack-start step. Direct port
// of deploy.py.
package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/nnnithinnn/slfhst/internal/config"
	"github.com/nnnithinnn/slfhst/internal/runx"
)

var imageLineRE = regexp.MustCompile(`(?m)^Image=(\S+)`)

// pullImages reads every rendered Quadlet .container file and `podman
// pull`s each distinct Image= it finds -- deliberately reads images back
// out of the already-rendered templates rather than hardcoding a list,
// matching deploy.py's pull_images().
func pullImages() error {
	quadletDir := filepath.Join(config.ServiceHomeDir, ".config/containers/systemd")
	matches, err := filepath.Glob(filepath.Join(quadletDir, "*.container"))
	if err != nil {
		return fmt.Errorf("deploy: glob %s: %w", quadletDir, err)
	}

	seen := map[string]bool{}
	var images []string
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("deploy: read %s: %w", path, err)
		}
		for _, m := range imageLineRE.FindAllSubmatch(data, -1) {
			image := string(m[1])
			if !seen[image] {
				seen[image] = true
				images = append(images, image)
			}
		}
	}

	for _, image := range images {
		if _, err := runx.Run([]string{"podman", "pull", image}, runx.Options{AsUser: config.ServiceUser}); err != nil {
			return fmt.Errorf("deploy: pull %s: %w", image, err)
		}
	}
	return nil
}

// startStack starts the bundling slfhst.target under the service user's
// systemd --user manager.
func startStack() error {
	_, err := runx.Run([]string{"machinectl", "shell", config.ServiceUser + "@",
		"/usr/bin/systemctl", "--user", "start", "slfhst.target"}, runx.Options{})
	if err != nil {
		return fmt.Errorf("deploy: start slfhst.target: %w", err)
	}
	return nil
}

// Run pulls every Quadlet-referenced image and starts the stack. Direct
// port of deploy.py's main().
func Run(store config.Store) error {
	if err := store.RequireDone("stage1"); err != nil {
		return err
	}
	if err := pullImages(); err != nil {
		return err
	}
	return startStack()
}
