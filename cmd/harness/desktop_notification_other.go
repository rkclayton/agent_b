//go:build !windows

package main

import (
	"errors"
	"harness/internal/notifications"
)

type desktopNotifier struct{}

func (desktopNotifier) Select(string)       {}
func (desktopNotifier) Viewing(string) bool { return false }
func (desktopNotifier) Show(notifications.DesktopNotice) error {
	return errors.New("desktop notifications require Windows")
}
