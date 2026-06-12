package testutil

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func DockerAvailable() error {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
			candidate := filepath.Join(runtimeDir, "docker.sock")
			if err := dialUnixSocket(candidate); err == nil {
				return nil
			}
		}
		host = "unix:///var/run/docker.sock"
	}

	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("parse docker host: %w", err)
	}

	switch u.Scheme {
	case "unix":
		return dialUnixSocket(u.Path)
	case "tcp", "http", "https":
		conn, err := net.DialTimeout("tcp", u.Host, time.Second)
		if err != nil {
			return err
		}
		return conn.Close()
	default:
		return fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
	}
}

func dialUnixSocket(path string) error {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}
