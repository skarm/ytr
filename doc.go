// Package ytr is a standalone client for every HTTP operation published in the
// Yandex Tracker API reference. It preserves custom fields and raw JSON rather
// than imposing a partial schema on a configurable Tracker installation.
//
// Clients may be shared by goroutines. Token sources and HTTP transports supplied
// by the application must also be safe for concurrent use. Files and mutable
// request bodies belong to the caller and must not be changed during a call.
package ytr

//go:generate go run ./internal/generate
