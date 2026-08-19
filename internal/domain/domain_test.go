package domain

import "testing"

func TestParseModeAcceptsOnlyMonitorAndApply(t *testing.T) {
	if mode, err := ParseMode("monitor"); err != nil || mode != ModeMonitor {
		t.Errorf("ParseMode(monitor) = %v, %v", mode, err)
	}
	if mode, err := ParseMode("apply"); err != nil || mode != ModeApply {
		t.Errorf("ParseMode(apply) = %v, %v", mode, err)
	}
	if _, err := ParseMode("yolo"); err == nil {
		t.Error("ParseMode(yolo) succeeded, want error")
	}
}

func TestParseBackendAcceptsTheThreeV1Backends(t *testing.T) {
	for raw, want := range map[string]Backend{
		"portainer":      BackendPortainer,
		"docker-compose": BackendDockerCompose,
		"podman-compose": BackendPodmanCompose,
	} {
		backend, err := ParseBackend(raw)
		if err != nil || backend != want {
			t.Errorf("ParseBackend(%q) = %v, %v; want %v", raw, backend, err, want)
		}
	}
	if _, err := ParseBackend("kubernetes"); err == nil {
		t.Error("ParseBackend(kubernetes) succeeded, want error")
	}
}
