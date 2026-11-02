package config

import (
	"strings"
	"testing"
)

const minimal = `{"worker_id":"taichi","control_plane":{"url":"http://127.0.0.1:7690/"},
	"reserve":{"mem_gib":16},"floor":{"disk_gib":50},"state_dir":"/var/lib/worker-daemon"}`

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.ControlPlane.URL != "http://127.0.0.1:7690" || c.ControlPlane.IdentityHeader != "X-Sortie-Worker-Identity" ||
		c.RunSlots != 1 || c.DiskPath != c.StateDir || c.Smolvm.Binary != "smolvm" || c.LogLevel != "info" ||
		c.ReserveMemGiB() != 16 || c.FloorDiskGiB() != 50 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestParseRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ doc, field string }{
		"no reserve":     {`{"worker_id":"w","control_plane":{"url":"http://h"},"floor":{"disk_gib":1},"state_dir":"/s"}`, "reserve.mem_gib"},
		"no floor":       {`{"worker_id":"w","control_plane":{"url":"http://h"},"reserve":{"mem_gib":1},"state_dir":"/s"}`, "floor.disk_gib"},
		"no url":         {`{"worker_id":"w","reserve":{"mem_gib":1},"floor":{"disk_gib":1},"state_dir":"/s"}`, "control_plane.url"},
		"user in url":    {`{"worker_id":"w","control_plane":{"url":"https://u:p@h"},"reserve":{"mem_gib":1},"floor":{"disk_gib":1},"state_dir":"/s"}`, "control_plane.url"},
		"cert over http": {`{"worker_id":"w","control_plane":{"url":"http://h","tls":{"cert_file":"/c","key_file":"/k"}},"reserve":{"mem_gib":1},"floor":{"disk_gib":1},"state_dir":"/s"}`, "control_plane.tls"},
		"cert, no key":   {`{"worker_id":"w","control_plane":{"url":"https://h","tls":{"cert_file":"/c"}},"reserve":{"mem_gib":1},"floor":{"disk_gib":1},"state_dir":"/s"}`, "control_plane.tls"},
		"negative":       {`{"worker_id":"w","control_plane":{"url":"http://h"},"reserve":{"mem_gib":-1},"floor":{"disk_gib":1},"state_dir":"/s"}`, "reserve"},
		"unknown field":  {`{"worker_id":"w","model_key":"x"}`, "model_key"},
		"bad worker id":  {`{"worker_id":"a b"}`, "worker_id"},
		"relative state": {`{"worker_id":"w","control_plane":{"url":"http://h"},"reserve":{"mem_gib":1},"floor":{"disk_gib":1},"state_dir":"s"}`, "state_dir"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err = %v, want one naming %s", err, tc.field)
			}
		})
	}
}

func TestPackAndUsageCommand(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(minimal, `"state_dir"`, `"smolvm":{"packs":{"ghcr.io/x/agent:1":"/packs/a.smolmachine"},"pack_dir":"/packs"},
		"usage_commands":{"opencode":["agent-usage","opencode"],"*":["agent-usage"]},"state_dir"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := c.Smolvm.Pack("ghcr.io/x/agent:1"); err != nil || p != "/packs/a.smolmachine" {
		t.Errorf("Pack = %s, %v", p, err)
	}
	if p, err := c.Smolvm.Pack("../../etc/b.smolmachine"); err != nil || p != "/packs/b.smolmachine" {
		t.Errorf("Pack by base name = %s, %v", p, err)
	}
	if _, err := c.Smolvm.Pack("ghcr.io/x/other:1"); err == nil {
		t.Error("an unknown image should have no pack")
	}
	if got := c.UsageCommand("/usr/bin/opencode"); len(got) != 2 {
		t.Errorf("opencode usage = %v", got)
	}
	if got := c.UsageCommand("claude-agent-acp"); len(got) != 1 {
		t.Errorf("default usage = %v", got)
	}
}

func TestParseImages(t *testing.T) {
	doc := strings.Replace(minimal, `"state_dir"`, `"smolvm":{"images":{"homelab.smolmachine":{"user":"builder","lib_dir":"/k",
		"mounts":[{"host":"/c/sources","guest":"/home/builder/.cache/homelab-sources","read_only":true},
		{"host":"/s/recorder","guest":"/var/log/homelab-recorder","per_job":true}],
		"env_files":{"HOMELAB_VM_AGE_KEY":"/s/inputs/vm-age/keys.txt"},"setup":["true"]}}},"state_dir"`, 1)
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	img := c.Smolvm.Images["homelab.smolmachine"]
	if img.User != "builder" || len(img.Mounts) != 2 || !img.Mounts[0].ReadOnly || !img.Mounts[1].PerJob ||
		img.EnvFiles["HOMELAB_VM_AGE_KEY"] == "" || len(img.Setup) != 1 {
		t.Errorf("image = %+v", img)
	}
	for name, tc := range map[string]struct{ image, field string }{
		"relative host":   {`{"mounts":[{"host":"c","guest":"/g"}]}`, "mounts[0]"},
		"colon":           {`{"mounts":[{"host":"/c:d","guest":"/g"}]}`, "mounts[0]"},
		"over workspace":  {`{"mounts":[{"host":"/c","guest":"/workspace/x"}]}`, "mounts[0].guest"},
		"over the share":  {`{"mounts":[{"host":"/c","guest":"/run"}]}`, "mounts[0].guest"},
		"bad variable":    {`{"env_files":{"A-B":"/f"}}`, "env_files"},
		"relative file":   {`{"env_files":{"A":"f"}}`, "env_files.A"},
		"relative kernel": {`{"lib_dir":"k"}`, "lib_dir"},
		"empty setup":     {`{"setup":[" "]}`, "setup[0]"},
		"unknown key":     {`{"net":"virtio"}`, "net"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := strings.Replace(minimal, `"state_dir"`, `"smolvm":{"images":{"i":`+tc.image+`}},"state_dir"`, 1)
			_, err := Parse([]byte(doc))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err = %v, want one naming %s", err, tc.field)
			}
		})
	}
}
