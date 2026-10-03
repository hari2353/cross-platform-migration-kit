package migration

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Setting struct {
	Recipe   string            `json:"recipe"`
	Values   map[string]string `json:"values"`
	Excluded []string          `json:"excluded,omitempty"`
}

func CaptureGitSetting(home string) (Setting, error) {
	setting := Setting{Recipe: "git-v1", Values: map[string]string{}}
	config, err := os.Open(filepath.Join(home, ".gitconfig"))
	if errors.Is(err, os.ErrNotExist) {
		return setting, nil
	}
	if err != nil {
		return Setting{}, err
	}
	defer config.Close()
	section := ""
	scanner := bufio.NewScanner(config)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		if section != "user" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "name" || key == "email" {
			setting.Values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Setting{}, err
	}
	return setting, nil
}

func RenderGitSetting(setting Setting) ([]byte, error) {
	if setting.Recipe != "git-v1" {
		return nil, errors.New("unsupported Git recipe")
	}
	for key := range setting.Values {
		if key != "name" && key != "email" {
			return nil, errors.New("Git recipe contains an unsupported value")
		}
	}
	return []byte("[user]\n\tname = " + setting.Values["name"] + "\n\temail = " + setting.Values["email"] + "\n"), nil
}
