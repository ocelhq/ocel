package infra

import (
	"encoding/json"
	"os"
)

const physicalPrefix = "physical-"

type Names struct {
	Declared string `json:"declared"`
	Bound    string `json:"bound"`
	Consumer string `json:"consumer,omitempty"`
}

func taskBindingKey(name string) string {
	return "OCEL_RESOURCE_TASK_" + name
}

func topicBindingKey(name string) string {
	return "OCEL_RESOURCE_TOPIC_" + name
}

func renameBindings(keys ...string) {
	for _, key := range keys {
		binding, ok := readBinding(key)
		if !ok {
			continue
		}
		var name string
		if json.Unmarshal(binding["name"], &name) != nil {
			continue
		}
		binding["name"], _ = json.Marshal(physicalPrefix + name)
		encoded, err := json.Marshal(binding)
		if err != nil {
			continue
		}
		_ = os.Setenv(key, string(encoded))
	}
}

func readBinding(key string) (map[string]json.RawMessage, bool) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return nil, false
	}
	var binding map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &binding) != nil {
		return nil, false
	}
	return binding, true
}

func readBoundName(key string) string {
	binding, _ := readBinding(key)
	var name string
	_ = json.Unmarshal(binding["name"], &name)
	return name
}

func ReadTaskNames() Names {
	return Names{Declared: ExactEcho.Name(), Bound: readBoundName(taskBindingKey(ExactEcho.Name()))}
}

func ReadTopicNames() Names {
	return Names{Declared: ExactOrders.Name(), Bound: readBoundName(topicBindingKey(ExactOrders.Name())), Consumer: ExactAudit.Name()}
}
