//go:build !unix

package staging

func syncDirectory(string) error { return nil }
