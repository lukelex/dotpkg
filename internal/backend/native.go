package backend

import (
	"context"
	"fmt"
	"strings"
)

// nativeResources contains Linux group and systemd operations shared by
// package-manager backends. Package-manager differences stay in each backend.
type nativeResources struct {
	Runner Runner
}

func (n *nativeResources) CurrentGroups(ctx context.Context, username string) ([]string, error) {
	output, err := n.Runner.Output(ctx, "id", []string{"-nG", username})
	if err != nil {
		return nil, fmt.Errorf("list groups for %s: %w", username, err)
	}
	return fields(string(output)), nil
}

func (n *nativeResources) GroupExists(ctx context.Context, group string) (bool, error) {
	_, err := n.Runner.Output(ctx, "getent", []string{"group", group})
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 2 {
		return false, nil
	}
	return false, fmt.Errorf("check group %s: %w", group, err)
}

func (n *nativeResources) AddToGroup(ctx context.Context, username, group string) error {
	if err := n.Runner.Run(ctx, "sudo", []string{"usermod", "-aG", group, username}, ""); err != nil {
		return fmt.Errorf("add %s to group %s: %w", username, group, err)
	}
	return nil
}

func (n *nativeResources) RemoveFromGroup(ctx context.Context, username, group string) error {
	if err := n.Runner.Run(ctx, "sudo", []string{"gpasswd", "-d", username, group}, ""); err != nil {
		return fmt.Errorf("remove %s from group %s: %w", username, group, err)
	}
	return nil
}

func (n *nativeResources) ServiceExists(ctx context.Context, user bool, service string) (bool, error) {
	_, err := n.Runner.Output(ctx, "systemctl", serviceArgs(user, "cat", service))
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check service %s: %w", service, err)
}

func (n *nativeResources) ServiceEnabled(ctx context.Context, user bool, service string) (bool, error) {
	_, err := n.Runner.Output(ctx, "systemctl", serviceArgs(user, "is-enabled", "--quiet", service))
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check enabled service %s: %w", service, err)
}

func (n *nativeResources) ReloadServices(ctx context.Context, user bool) error {
	if err := n.runSystemctl(ctx, user, "daemon-reload"); err != nil {
		return fmt.Errorf("reload services: %w", err)
	}
	return nil
}

func (n *nativeResources) RestartService(ctx context.Context, user bool, service string) error {
	if err := n.runSystemctl(ctx, user, "try-restart", service); err != nil {
		return fmt.Errorf("restart service %s: %w", service, err)
	}
	return nil
}

func (n *nativeResources) EnableService(ctx context.Context, user bool, service string) error {
	if err := n.runSystemctl(ctx, user, "enable", "--now", service); err != nil {
		return fmt.Errorf("enable service %s: %w", service, err)
	}
	return nil
}

func (n *nativeResources) DisableService(ctx context.Context, user bool, service string) error {
	if err := n.runSystemctl(ctx, user, "disable", "--now", service); err != nil {
		return fmt.Errorf("disable service %s: %w", service, err)
	}
	return nil
}

func (n *nativeResources) runSystemctl(ctx context.Context, user bool, args ...string) error {
	args = serviceArgs(user, args...)
	if user {
		return n.Runner.Run(ctx, "systemctl", args, "")
	}
	return n.Runner.Run(ctx, "sudo", append([]string{"systemctl"}, args...), "")
}

func fields(value string) []string {
	return strings.Fields(value)
}
