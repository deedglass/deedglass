//go:build !unix

package main

func lockInit(string) (func(), error) { return func() {}, nil }
