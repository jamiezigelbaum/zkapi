package main

var version = "dev"

func supportedPlatform(goos, goarch string) bool { return goos == "linux" && goarch == "amd64" }
