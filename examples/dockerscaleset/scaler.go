package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	dockerclient "github.com/docker/docker/client"
	"github.com/google/uuid"
)

// dindImage is pulled once at startup; ContainerCreate does not pull.
const dindImage = "docker:dind"

// ownerLabel marks every dind container and volume with the runner it belongs
// to. Ownership must live on the Docker side, not only in runnerState's
// in-memory maps: the maps die with the process, and resources only they knew
// about are never collected — the failure behind the 422 GB orphan incident.
const ownerLabel = "dockerscaleset.runner"

type Scaler struct {
	// setupMu serialises startRunner against reapOrphans: mid-setup a runner's
	// dind and volumes exist while the runner container does not yet, which is
	// exactly what the sweep reads as an orphan.
	setupMu        sync.Mutex
	runners        runnerState
	runnerImage    string
	scaleSetID     int
	dockerClient   *dockerclient.Client
	scalesetClient *scaleset.Client
	minRunners     int
	maxRunners     int
	logger         *slog.Logger
}

func (a *Scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	currentCount := a.runners.count()
	targetRunnerCount := min(a.maxRunners, a.minRunners+count)

	switch {
	case targetRunnerCount == currentCount:
		// No scaling needed
		return currentCount, nil
	case targetRunnerCount > currentCount:
		// Scale up
		scaleUp := targetRunnerCount - currentCount
		a.logger.Info(
			"Scaling up runners",
			slog.Int("currentCount", currentCount),
			slog.Int("desiredCount", targetRunnerCount),
			slog.Int("scaleUp", scaleUp),
		)

		for range scaleUp {
			if _, err := a.startRunner(ctx); err != nil {
				return 0, fmt.Errorf("failed to start runner: %w", err)
			}
		}

		return a.runners.count(), nil
	default:
		// No need to handle scale down events, since:
		// 1. JobCompleted events will first remove runners
		// 2. If the count is still below the current runner count, the JobCompleted event will be delivered in the next batch.
		// 3. Removal after JobCompleted events is handled synchronously.
		// 4. If the job is cancelled, the JobCompleted event will still be delivered.
	}
	return a.runners.count(), nil
}

func (a *Scaler) HandleJobStarted(ctx context.Context, jobInfo *scaleset.JobStarted) error {
	a.logger.Info(
		"Job started",
		slog.Int64("runnerRequestId", jobInfo.RunnerRequestID),
		slog.String("jobId", jobInfo.JobID),
	)
	a.runners.markBusy(jobInfo.RunnerName)
	return nil
}

func (a *Scaler) HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error {
	a.logger.Info("Job completed", slog.Int64("runnerRequestId", jobInfo.RunnerRequestID), slog.String("jobId", jobInfo.JobID))

	containerID := a.runners.markDone(jobInfo.RunnerName)
	// Not fatal. With AutoRemove the daemon owns the removal and this call is
	// only belt-and-braces, so it can lose the race several ways — the
	// container is already gone, or the daemon's own removal is mid-flight
	// ("removal of container X is already in progress", a 409, not a 404).
	// Returning any of those kills the listener, and the shutdown that follows
	// force-removes every busy runner, so one lost race fails every job then
	// running.
	if err := a.removeContainer(ctx, containerID); err != nil {
		a.logger.Warn(
			"Runner container removal failed; the daemon's AutoRemove is authoritative",
			slog.String("name", jobInfo.RunnerName),
			slog.String("containerID", containerID),
			slog.String("error", err.Error()),
		)
	}

	a.removeDindResources(ctx, jobInfo.RunnerName)

	return nil
}

// removeContainer deletes a runner container, treating an already-removed one
// as success — under AutoRemove that is the normal outcome, not a failure.
func (a *Scaler) removeContainer(ctx context.Context, containerID string) error {
	err := a.dockerClient.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if err != nil && !dockerclient.IsErrNotFound(err) {
		return err
	}
	return nil
}

func (a *Scaler) startRunner(ctx context.Context) (string, error) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()

	name := fmt.Sprintf("runner-%s", uuid.NewString()[:8])

	jit, err := a.scalesetClient.GenerateJitRunnerConfig(
		ctx,
		&scaleset.RunnerScaleSetJitRunnerSetting{
			Name: name,
		},
		a.scaleSetID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate JIT config: %w", err)
	}

	// Create shared volumes for the dind sidecar architecture.
	volSock := name + "-sock"
	volWork := name + "-work"
	volExternals := name + "-externals"

	cleanup := func() {
		a.removeDindResources(context.WithoutCancel(ctx), name)
	}

	for _, v := range []string{volSock, volWork, volExternals} {
		if _, err := a.dockerClient.VolumeCreate(ctx, volume.CreateOptions{
			Name:   v,
			Labels: map[string]string{ownerLabel: name},
		}); err != nil {
			cleanup()
			return "", fmt.Errorf("failed to create volume %s: %w", v, err)
		}
	}

	// Init: copy runner externals into the shared volume.
	if err := a.runInitContainer(ctx, name, volExternals); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to run init container: %w", err)
	}

	// Start the dind sidecar.
	if err := a.startDind(ctx, name, volSock, volWork, volExternals); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to start dind container: %w", err)
	}

	// Wait for dockerd inside dind to become ready.
	if err := a.waitForDind(ctx, name+"-dind"); err != nil {
		cleanup()
		return "", fmt.Errorf("dind not ready: %w", err)
	}

	dindMounts := []mount.Mount{
		{Type: mount.TypeVolume, Source: volSock, Target: "/var/run"},
		{Type: mount.TypeVolume, Source: volWork, Target: "/home/runner/_work"},
	}

	c, err := a.dockerClient.ContainerCreate(
		ctx,
		&container.Config{
			Image: a.runnerImage,
			User:  "runner",
			Cmd:   []string{"/home/runner/run.sh"},
			Env: []string{
				fmt.Sprintf("ACTIONS_RUNNER_INPUT_JITCONFIG=%s", jit.EncodedJITConfig),
			},
		},
		&container.HostConfig{
			// A JIT runner is single-use, so the daemon can reap it the moment it
			// exits. Without this the only collector is this process, and anything
			// it loses track of — every container alive across a controller restart
			// — leaks forever: 70 orphans held 422 GB on the on-prem host and
			// filled its disk.
			AutoRemove: true,
			Binds: []string{
				"/var/cache/pip:/home/runner/.cache/pip",
				"/var/cache/uv:/home/runner/.cache/uv",
				"/var/cache/npm:/home/runner/.npm",
				"/var/cache/yarn:/home/runner/.cache/yarn",
				"/var/cache/ms-playwright:/home/runner/.cache/ms-playwright",
			},
			CapAdd: []string{"NET_ADMIN"},
			Mounts: dindMounts,
			Resources: container.Resources{
				Devices: []container.DeviceMapping{
					{PathOnHost: "/dev/net/tun", PathInContainer: "/dev/net/tun", CgroupPermissions: "rwm"},
				},
			},
		},
		nil, nil,
		name,
	)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("failed to create runner container: %w", err)
	}

	if err := a.dockerClient.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
		// A created-but-never-started container never exits, so AutoRemove
		// never fires — without this it leaks and pins the volumes with it.
		if rmErr := a.dockerClient.ContainerRemove(context.WithoutCancel(ctx), c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			a.logger.Warn("Failed to remove unstarted runner container", slog.String("name", name), slog.String("error", rmErr.Error()))
		}
		cleanup()
		return "", fmt.Errorf("failed to start runner container: %w", err)
	}

	a.runners.addIdle(name, c.ID)
	return name, nil
}

// runInitContainer copies runner externals into the shared volume so dind can
// access them.
func (a *Scaler) runInitContainer(ctx context.Context, name, volExternals string) error {
	initName := name + "-init"
	c, err := a.dockerClient.ContainerCreate(
		ctx,
		&container.Config{
			Image:  a.runnerImage,
			Cmd:    []string{"cp", "-r", "/home/runner/externals/.", "/home/runner/tmpDir/"},
			Labels: map[string]string{ownerLabel: name},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volExternals, Target: "/home/runner/tmpDir"},
			},
		},
		nil, nil,
		initName,
	)
	if err != nil {
		return fmt.Errorf("failed to create init container: %w", err)
	}
	defer func() {
		_ = a.dockerClient.ContainerRemove(context.WithoutCancel(ctx), c.ID, container.RemoveOptions{Force: true})
	}()

	if err := a.dockerClient.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start init container: %w", err)
	}

	waitCh, errCh := a.dockerClient.ContainerWait(ctx, c.ID, container.WaitConditionNotRunning)
	select {
	case result := <-waitCh:
		if result.StatusCode != 0 {
			return fmt.Errorf("init container exited with status %d", result.StatusCode)
		}
	case err := <-errCh:
		return fmt.Errorf("waiting for init container: %w", err)
	}
	return nil
}

func (a *Scaler) startDind(ctx context.Context, name, volSock, volWork, volExternals string) error {
	dindName := name + "-dind"
	// Paths must match between dind and runner so daemon bind mounts resolve correctly.
	c, err := a.dockerClient.ContainerCreate(
		ctx,
		&container.Config{
			Image: dindImage,
			// --group=123: matches the docker group gid inside the runner image.
			Cmd:    []string{"dockerd", "--host=unix:///var/run/docker.sock", "--group=123"},
			Labels: map[string]string{ownerLabel: name},
		},
		&container.HostConfig{
			Privileged: true,
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: volSock, Target: "/var/run"},
				{Type: mount.TypeVolume, Source: volWork, Target: "/home/runner/_work"},
				{Type: mount.TypeVolume, Source: volExternals, Target: "/home/runner/externals"},
			},
		},
		nil, nil,
		dindName,
	)
	if err != nil {
		return fmt.Errorf("failed to create dind container: %w", err)
	}

	if err := a.dockerClient.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start dind container: %w", err)
	}
	return nil
}

// waitForDind polls "docker info" inside the dind container until dockerd is
// ready, mirroring ARC's startupProbe (failureThreshold=24, periodSeconds=5).
func (a *Scaler) waitForDind(ctx context.Context, dindName string) error {
	const maxRetries = 24
	const interval = 5 * time.Second

	for i := range maxRetries {
		ready, err := a.probeDind(ctx, dindName)
		if ready {
			a.logger.Info("dind ready", slog.String("container", dindName), slog.Int("attempts", i+1))
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.logger.Debug("dind probe failed", slog.Int("attempt", i+1), slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("dind container %s not ready after %d attempts", dindName, maxRetries)
}

// probeDind runs "docker info" in the container and reports whether it exited
// zero. ExecStart returns before the command finishes, so the same exec is
// polled until it stops running — inspecting immediately races the command.
func (a *Scaler) probeDind(ctx context.Context, dindName string) (bool, error) {
	exec, err := a.dockerClient.ContainerExecCreate(ctx, dindName, container.ExecOptions{
		Cmd: []string{"docker", "info"},
	})
	if err != nil {
		return false, err
	}
	if err := a.dockerClient.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return false, err
	}
	for {
		inspect, err := a.dockerClient.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return false, err
		}
		if !inspect.Running {
			return inspect.ExitCode == 0, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// removeDindResources force-removes the dind container, a leftover init
// container, and the shared volumes. Failures are logged but never propagated.
func (a *Scaler) removeDindResources(ctx context.Context, runnerName string) {
	for _, c := range []string{runnerName + "-dind", runnerName + "-init"} {
		if err := a.dockerClient.ContainerRemove(ctx, c, container.RemoveOptions{Force: true}); err != nil && !dockerclient.IsErrNotFound(err) {
			a.logger.Warn("Failed to remove container", slog.String("name", c), slog.String("error", err.Error()))
		}
	}
	for _, v := range []string{runnerName + "-sock", runnerName + "-work", runnerName + "-externals"} {
		if err := a.dockerClient.VolumeRemove(ctx, v, true); err != nil {
			a.logger.Warn("Failed to remove volume", slog.String("volume", v), slog.String("error", err.Error()))
		}
	}
}

// reapOrphans collects dind containers and volumes whose runner is gone. After
// a crash the in-memory ownership maps are empty, runner containers reap
// themselves through AutoRemove, and their sidecars and volumes would leak
// forever. The runner container doubles as a liveness anchor: while it exists
// its job may still be running, so its resources are left alone.
func (a *Scaler) reapOrphans(ctx context.Context) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()

	labelFilter := filters.NewArgs(filters.Arg("label", ownerLabel))

	owners := map[string]bool{}
	containers, err := a.dockerClient.ContainerList(ctx, container.ListOptions{All: true, Filters: labelFilter})
	if err != nil {
		a.logger.Warn("Orphan sweep: listing containers failed", slog.String("error", err.Error()))
	}
	for _, c := range containers {
		if name := c.Labels[ownerLabel]; name != "" {
			owners[name] = true
		}
	}

	volumes, err := a.dockerClient.VolumeList(ctx, volume.ListOptions{Filters: labelFilter})
	if err != nil {
		a.logger.Warn("Orphan sweep: listing volumes failed", slog.String("error", err.Error()))
	}
	for _, v := range volumes.Volumes {
		if name := v.Labels[ownerLabel]; name != "" {
			owners[name] = true
		}
	}

	for name := range owners {
		if _, err := a.dockerClient.ContainerInspect(ctx, name); err == nil {
			continue // runner still exists; its job may be running
		} else if !dockerclient.IsErrNotFound(err) {
			a.logger.Warn("Orphan sweep: inspect failed, skipping", slog.String("runner", name), slog.String("error", err.Error()))
			continue
		}
		a.logger.Info("Reaping orphaned dind resources", slog.String("runner", name))
		a.removeDindResources(ctx, name)
	}
}

func (a *Scaler) shutdown(ctx context.Context) {
	a.logger.Info("Shutting down runners")
	a.runners.mu.Lock()
	defer a.runners.mu.Unlock()

	for name, containerID := range a.runners.idle {
		a.logger.Info("Removing idle runner", slog.String("name", name), slog.String("containerID", containerID))
		if err := a.removeContainer(ctx, containerID); err != nil {
			a.logger.Error("Failed to remove idle runner container", slog.String("name", name), slog.String("containerID", containerID), slog.String("error", err.Error()))
		}
		a.removeDindResources(ctx, name)
	}
	clear(a.runners.idle)

	for name, containerID := range a.runners.busy {
		a.logger.Info("Removing busy runner", slog.String("name", name), slog.String("containerID", containerID))
		if err := a.removeContainer(ctx, containerID); err != nil {
			a.logger.Error("Failed to remove busy runner container", slog.String("name", name), slog.String("containerID", containerID), slog.String("error", err.Error()))
		}
		a.removeDindResources(ctx, name)
	}
	clear(a.runners.busy)
}

var _ listener.Scaler = (*Scaler)(nil)

type runnerState struct {
	mu   sync.Mutex
	idle map[string]string
	busy map[string]string
}

func (r *runnerState) count() int {
	r.mu.Lock()
	count := len(r.idle) + len(r.busy)
	r.mu.Unlock()
	return count
}

func (r *runnerState) markBusy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.idle[name]
	if !ok {
		panic("marking non-existent runner busy")
	}
	delete(r.idle, name)
	r.busy[name] = state
}

func (r *runnerState) markDone(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.markDoneUnlocked(name)
}

func (r *runnerState) markDoneUnlocked(name string) string {
	containerID, ok := r.busy[name]
	if ok {
		delete(r.busy, name)
		return containerID
	}
	containerID, ok = r.idle[name]
	if ok {
		delete(r.idle, name)
		return containerID
	}
	panic("marking non-existent runner done")
}

func (r *runnerState) addIdle(name, containerID string) {
	r.mu.Lock()
	r.idle[name] = containerID
	r.mu.Unlock()
}
