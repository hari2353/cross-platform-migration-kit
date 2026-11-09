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
		if section != "user" && section != "alias" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if section == "user" {
			if key == "name" || key == "email" {
				setting.Values[key] = value
			}
			continue
		}
		// Alias sections travel as prefixed keys so renders can rebuild
		// them under [alias].
		if key != "" {
			setting.Values["alias."+key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Setting{}, err
	}
	return setting, nil
}

func RenderGitSetting(setting Setting, osArgs ...string) ([]byte, error) {
	if setting.Recipe != "git-v1" {
		return nil, errors.New("unsupported Git recipe")
	}
	dstOS := ""
	if len(osArgs) >= 2 { dstOS = osArgs[1] }
	allowed := map[string]bool{"name": true, "email": true, "autocrlf": true, "alias.st": true, "defaultbranch": true}
	for key := range setting.Values {
		if !allowed[key] {
			return nil, errors.New("Git recipe contains an unsupported value")
		}
	}
	var b strings.Builder
	// user section
	if v, ok := setting.Values["name"]; ok {
		b.WriteString("[user]\n\tname = ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	if v, ok := setting.Values["email"]; ok {
		if !strings.Contains(b.String(), "[user]") {
			b.WriteString("[user]\n")
		}
		b.WriteString("\temail = ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	// autocrlf (transform based on destination OS)
	if v, ok := setting.Values["autocrlf"]; ok {
		out := v
		if dstOS == "darwin" {
			out = "input"
		} else if dstOS == "windows" {
			out = "true"
		}
		b.WriteString("[core]\n\tautocrlf = ")
		b.WriteString(out)
		b.WriteString("\n")
	}
	// alias
	if v, ok := setting.Values["alias.st"]; ok {
		b.WriteString("[alias]\n\tst = ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	// default branch
	if v, ok := setting.Values["defaultbranch"]; ok {
		// render as defaultBranch = value
		b.WriteString("defaultBranch = ")
		b.WriteString(v)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}
