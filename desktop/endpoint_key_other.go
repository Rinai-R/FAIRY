//go:build !unix

package main

import "io/fs"

func validateEndpointKeyOwner(string, fs.FileInfo) error {
	return nil
}
