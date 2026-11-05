package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

const PortableName = "site-agent.json"

// Find checks the executable's own directory, never an unrelated working directory.
// Identity and private keys stay in the per-device data directory.
func Find(executable, directory string) (string, error) {
	candidates := []string{filepath.Join(filepath.Dir(executable), PortableName), filepath.Join(directory, PortableName), filepath.Join(directory, "agent-ui.json")}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("Agent 配置不是普通文件：%s", path)
		}
		return filepath.Abs(path)
	}
	return "", nil
}

func Discover(directory string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return Find(executable, directory)
}

func Save(path string, settings Settings) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	data, err := Marshal(settings)
	if err != nil {
		return err
	}
	return WritePrivate(path, data)
}
