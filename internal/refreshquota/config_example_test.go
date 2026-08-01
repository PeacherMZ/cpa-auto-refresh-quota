package refreshquota

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepositoryConfigExampleIsValidAndSafeByDefault(t *testing.T) {
	raw, errRead := os.ReadFile("../../config.example.yaml")
	if errRead != nil {
		t.Fatalf("read config.example.yaml: %v", errRead)
	}

	var document struct {
		Plugins struct {
			Configs map[string]any `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if errDecode := yaml.Unmarshal(raw, &document); errDecode != nil {
		t.Fatalf("decode config.example.yaml: %v", errDecode)
	}

	pluginRaw, exists := document.Plugins.Configs["cpa-auto-refresh-quota"]
	if !exists {
		t.Fatal("config.example.yaml does not contain cpa-auto-refresh-quota")
	}
	encoded, errEncode := yaml.Marshal(pluginRaw)
	if errEncode != nil {
		t.Fatalf("encode plugin config: %v", errEncode)
	}

	config, errParse := ParseConfig(encoded)
	if errParse != nil {
		t.Fatalf("parse plugin config: %v", errParse)
	}
	if config.ScheduleEnabled {
		t.Fatal("repository example must keep schedule_enabled disabled")
	}
	if config.TemporaryPriority {
		t.Fatal("repository example must keep temporary_priority_override disabled")
	}
}
