package infra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/stupidprogrammer4/tidelab/internal/modules/system/domain"
)

const maxConfigBytes = 64 << 10

func DefaultConfig() domain.Config {
	return domain.Config{DataDir: ".tidelab", ListenAddr: "127.0.0.1:8080"}
}

// LoadConfig reads optional JSON settings and rejects unknown or trailing data.
func LoadConfig(path string) (domain.Config, error) {
	config := DefaultConfig()
	if path == "" {
		return config, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return domain.Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return domain.Config{}, fmt.Errorf("read config: %w", err)
	}
	if len(content) > maxConfigBytes {
		return domain.Config{}, errors.New("invalid config: file exceeds 64 KiB")
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return domain.Config{}, fmt.Errorf("parse config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return domain.Config{}, errors.New("parse config: trailing JSON value")
		}
		return domain.Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := validateConfig(config); err != nil {
		return domain.Config{}, err
	}
	return config, nil
}

func validateConfig(config domain.Config) error {
	if config.DataDir == "" || filepath.Clean(config.DataDir) == "." {
		return errors.New("invalid config: data_dir must name a directory")
	}
	host, portText, err := net.SplitHostPort(config.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid config: listen_addr: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("invalid config: listen_addr must use a loopback host")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("invalid config: listen_addr must have a numeric port from 0 to 65535")
	}
	return nil
}
