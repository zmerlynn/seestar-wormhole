//go:build !windows || !gui

package main

import "os/signal"

var notifyContext = signal.NotifyContext
