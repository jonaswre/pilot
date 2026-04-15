package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// executorImageTag returns the Docker image tag for a project-specific executor.
// Format: pilot-executor/<sanitized-name>:latest
func executorImageTag(projectName string) string {
	return "pilot-executor/" + sanitizeImageName(projectName) + ":latest"
}

// sanitizeImageName converts a project name to a valid Docker image name component.
// Lowercase, replace non-alphanumeric with hyphens, trim.
func sanitizeImageName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "project"
	}
	re := regexp.MustCompile(`[^a-z0-9-]`)
	return re.ReplaceAllString(name, "-")
}

// executorImageExists checks if a Docker image exists locally.
func executorImageExists(ctx context.Context, imageName string) bool {
	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: "docker",
		Args:    []string{"image", "inspect", imageName},
	})
	if err != nil {
		return false
	}
	io.Copy(io.Discard, running.Stdout)
	io.Copy(io.Discard, running.Stderr)
	return running.Wait() == nil
}

// buildMu serializes image builds per project name to prevent concurrent builds.
var buildMu sync.Map // map[string]*sync.Mutex

func getBuildMutex(projectName string) *sync.Mutex {
	val, _ := buildMu.LoadOrStore(projectName, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// buildExecutorImage builds a project-specific executor image from a Dockerfile.
// Returns the image tag on success.
func buildExecutorImage(ctx context.Context, projectPath, projectName, dockerfilePath string) (string, error) {
	mu := getBuildMutex(projectName)
	mu.Lock()
	defer mu.Unlock()

	tag := executorImageTag(projectName)

	// Re-check after acquiring lock (another goroutine may have built it)
	if executorImageExists(ctx, tag) {
		return tag, nil
	}

	dfPath := dockerfilePath
	if dfPath == "" {
		dfPath = "Dockerfile.executor"
	}
	fullDfPath := filepath.Join(projectPath, dfPath)

	slog.Info("Building project-specific executor image",
		slog.String("tag", tag),
		slog.String("dockerfile", fullDfPath),
		slog.String("context", projectPath),
	)

	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: "docker",
		Args: []string{
			"build",
			"-t", tag,
			"-f", fullDfPath,
			projectPath,
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to start docker build: %w", err)
	}

	// Capture build output for logging
	var stderr bytes.Buffer
	io.Copy(io.Discard, running.Stdout)
	io.Copy(&stderr, running.Stderr)

	if err := running.Wait(); err != nil {
		return "", fmt.Errorf("docker build failed: %w\nstderr: %s", err, stderr.String())
	}

	slog.Info("Built project-specific executor image", slog.String("tag", tag))
	return tag, nil
}

// resolveExecutorImage determines which container image to use for a task.
// Priority:
//  1. opts.ExecutorImage (per-project override from config)
//  2. Auto-built image (pilot-executor/<project>:latest) if it exists or can be built
//  3. config.Image (global default, e.g., "pilot/executor:latest")
func resolveExecutorImage(ctx context.Context, config *OpenSandboxIsolationConfig, opts IsolationOpts) string {
	// 1. Explicit per-project override
	if opts.ExecutorImage != "" {
		slog.Info("Using per-project executor image override",
			slog.String("image", opts.ExecutorImage),
		)
		return opts.ExecutorImage
	}

	// 2. Auto-built image
	projectName := opts.ProjectName
	if projectName == "" && opts.ProjectPath != "" {
		projectName = filepath.Base(opts.ProjectPath)
	}

	if projectName != "" {
		tag := executorImageTag(projectName)

		// Check if auto-built image already exists
		if executorImageExists(ctx, tag) {
			slog.Info("Using existing project-specific executor image",
				slog.String("image", tag),
			)
			return tag
		}

		// Try auto-build if enabled and Dockerfile exists
		if config.IsAutoBuildEnabled() && opts.ProjectPath != "" {
			dfPath := config.DockerfilePath
			if dfPath == "" {
				dfPath = "Dockerfile.executor"
			}
			fullDfPath := filepath.Join(opts.ProjectPath, dfPath)

			if _, err := os.Stat(fullDfPath); err == nil {
				built, err := buildExecutorImage(ctx, opts.ProjectPath, projectName, dfPath)
				if err != nil {
					slog.Warn("Auto-build failed, falling back to global image",
						slog.String("project", projectName),
						slog.Any("error", err),
					)
				} else {
					return built
				}
			}
		}
	}

	// 3. Global default
	return config.Image
}
