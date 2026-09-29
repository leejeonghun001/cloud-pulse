package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestListeningPortJSON(t *testing.T) {
	t.Parallel()

	p := ListeningPort{
		Proto:       "tcp",
		IP:          "0.0.0.0",
		Port:        8080,
		PID:         1234,
		Process:     "nginx",
		ContainerID: "abcdef012345",
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ListeningPort
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, p) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, p)
	}
}

func TestListeningPortJSON_ContainerIDOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	p := ListeningPort{Proto: "udp", IP: "127.0.0.1", Port: 53}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["container_id"]; ok {
		t.Errorf("container_id must be omitted when empty, got %s", b)
	}
}

func TestContainerJSON(t *testing.T) {
	t.Parallel()

	c := Container{
		ID:             "abcdef012345",
		Name:           "web",
		Image:          "nginx:latest",
		State:          "running",
		Status:         "Up 3 days (healthy)",
		Health:         "healthy",
		CreatedAt:      1700000000,
		ComposeProject: "myapp",
		ComposeService: "web",
		Ports: []ContainerPort{
			{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Container
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, c) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, c)
	}
}

func TestContainerJSON_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	t.Parallel()

	c := Container{ID: "abc", Name: "n", Image: "i", State: "running", Status: "Up"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{"health", "compose_project", "compose_service"} {
		if _, ok := raw[field]; ok {
			t.Errorf("%s must be omitted when empty, got %s", field, b)
		}
	}
	if _, ok := raw["ports"]; !ok {
		t.Errorf("ports must always be present, got %s", b)
	}
}

func TestContainerPortJSON_PublicPortOmittedWhenZero(t *testing.T) {
	t.Parallel()

	p := ContainerPort{PrivatePort: 443, Type: "tcp"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["public_port"]; ok {
		t.Errorf("public_port must be omitted when zero, got %s", b)
	}
	if _, ok := raw["ip"]; ok {
		t.Errorf("ip must be omitted when empty, got %s", b)
	}
}

func TestDockerInfoJSON(t *testing.T) {
	t.Parallel()

	di := DockerInfo{
		Status:  DockerStatusOK,
		Version: "1.41",
		Containers: []Container{
			{ID: "abc", Name: "web", Image: "nginx", State: "running", Status: "Up", Ports: []ContainerPort{}},
		},
	}
	b, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded DockerInfo
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, di) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, di)
	}
}

func TestDockerInfoJSON_ErrorOmittedWhenOK(t *testing.T) {
	t.Parallel()

	di := DockerInfo{Status: DockerStatusOK, Version: "1.41", Containers: []Container{}}
	b, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["error"]; ok {
		t.Errorf("error must be omitted when Status is ok, got %s", b)
	}
}

func TestDockerStatusConstants(t *testing.T) {
	t.Parallel()

	want := map[DockerStatus]string{
		DockerStatusOK:               "ok",
		DockerStatusUnavailable:      "unavailable",
		DockerStatusPermissionDenied: "permission_denied",
		DockerStatusError:            "error",
		DockerStatusUnsupported:      "unsupported",
	}
	for status, str := range want {
		if string(status) != str {
			t.Errorf("%v = %q, want %q", status, string(status), str)
		}
	}
}

func TestInventoryJSON(t *testing.T) {
	t.Parallel()

	inv := Inventory{
		CollectedAt: 1700000000,
		Ports: []ListeningPort{
			{Proto: "tcp", IP: "0.0.0.0", Port: 22, PID: 1, Process: "sshd"},
		},
		Docker: DockerInfo{Status: DockerStatusUnavailable, Error: "no socket", Containers: []Container{}},
	}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Inventory
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, inv) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, inv)
	}
}

func TestInventoryJSON_EmptyPortsEncodedAsArray(t *testing.T) {
	t.Parallel()

	inv := Inventory{CollectedAt: 100, Ports: []ListeningPort{}, Docker: DockerInfo{Status: DockerStatusUnavailable, Containers: []Container{}}}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if string(raw["ports"]) != "[]" {
		t.Errorf("ports = %s, want []", raw["ports"])
	}
}

func TestInventoryCapsConstants(t *testing.T) {
	t.Parallel()

	if MaxInventoryPorts != 1000 {
		t.Errorf("MaxInventoryPorts = %d, want 1000", MaxInventoryPorts)
	}
	if MaxInventoryContainers != 500 {
		t.Errorf("MaxInventoryContainers = %d, want 500", MaxInventoryContainers)
	}
}
