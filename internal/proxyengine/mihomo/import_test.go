package mihomo

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/common/utils"
)

const basicSS = "ss://YWVzLTEyOC1nY206cHJpdmF0ZS1wYXNz@Example.COM:1234#private-name"

func TestImportFormatsCanonicalAndPrivate(t *testing.T) {
	inputs := []string{
		basicSS,
		base64.StdEncoding.EncodeToString([]byte(basicSS)),
		"proxies:\n  - {name: renamed, type: ss, server: example.com., port: '1234', cipher: aes-128-gcm, password: private-pass, udp: false}\nrules: [MATCH,DIRECT]\ntun: {enable: true}\n",
	}
	var canonical string
	for _, input := range inputs {
		r, err := Import([]byte(input))
		if err != nil || r.Accepted != 1 || r.Failed != 0 {
			t.Fatalf("counts: %d/%d, %v", r.Accepted, r.Failed, err)
		}
		data, _ := json.Marshal(r.Nodes)
		if canonical == "" {
			canonical = string(data)
		} else if canonical != string(data) {
			t.Fatal("equivalent input changed connection identity")
		}
		public, _ := json.Marshal(r)
		for _, secret := range []string{"private", "example", "password", "nodes"} {
			if strings.Contains(string(public), secret) {
				t.Fatalf("public report exposed %s", secret)
			}
		}
	}
	r, err := Import([]byte(basicSS + "\n" + strings.ReplaceAll(basicSS, "private-name", "new-name") + "\nvmess://private-unsupported\nvless://private@host?ed=bad"))
	if err != nil || r.Accepted != 1 || r.Skipped != 1 || r.Failed != 2 {
		t.Fatalf("wrong partial report: %+v %v", r.Items, err)
	}
}

func TestImportURIProtocolBoundaries(t *testing.T) {
	for _, input := range []string{
		"vless://00000000-0000-0000-0000-000000000001@localhost:1234?type=tcp&security=tls&sni=localhost",
		"anytls://private-pass@localhost:1234?sni=localhost",
		"hy2://private%40pass@localhost:1234?sni=localhost",
	} {
		r, err := Import([]byte(input))
		if err != nil || r.Accepted != 1 {
			t.Fatalf("supported URI: %+v %v", r.Items, err)
		}
		if strings.HasPrefix(input, "hy2") && r.Nodes[0]["password"] != "private@pass" {
			t.Fatal("escaped password not decoded")
		}
	}
	for _, input := range []string{
		basicSS + "&oops", // this is a fragment/name and is intentionally harmless
	} {
		if r, _ := Import([]byte(input)); r.Accepted != 1 {
			t.Fatal("display data affects parse")
		}
	}
	for _, input := range []string{
		"vless://private@localhost:1234?type=ws&ed=private-invalid",
		"vless://private@localhost:1234?security=reality",
		"vless://private@localhost:1234?unrecognized=secret",
		"ss://YWVzLTEyOC1nY206cHJpdmF0ZS1wYXNz@localhost:1234?plugin=unknown",
		"hy2://private@localhost:1234?obfs=salamander&obfs-password=private",
		"proxies: [{type: ss, server: localhost, port: 1234, cipher: aes-128-gcm, password: private, interface-name: eth0}]",
		"proxies: [{type: ss, server: localhost, port: 1234, cipher: aes-128-gcm, password: private, dialer-proxy: other}]",
	} {
		r, err := Import([]byte(input))
		if err != nil || r.Accepted != 0 || r.Failed != 1 {
			t.Fatalf("unsupported input accepted: %+v %v", r.Items, err)
		}
	}
}

func TestImportBudget(t *testing.T) {
	for _, data := range []string{"", strings.Repeat("x", MaxImportBytes+1), strings.Repeat(basicSS+"\n", MaxImportNodes+1)} {
		if _, err := Import([]byte(data)); err == nil {
			t.Fatal("unbounded import accepted")
		}
	}
}

func TestImportVLESSIdentityPreservesCustomIDs(t *testing.T) {
	const canonical = "d342d11e-d424-4583-b36e-524ab1f0afa4"
	for _, format := range []string{"uri", "yaml"} {
		t.Run(format, func(t *testing.T) {
			var lines []string
			if format == "yaml" {
				lines = append(lines, "proxies:")
			}
			for _, id := range []string{"UserA", "usera", canonical, strings.ToUpper(canonical)} {
				if format == "yaml" {
					lines = append(lines, "  - {type: vless, server: localhost, port: 1234, uuid: "+id+"}")
				} else {
					lines = append(lines, "vless://"+id+"@localhost:1234")
				}
			}
			r, err := Import([]byte(strings.Join(lines, "\n")))
			if err != nil || r.Accepted != 3 || r.Skipped != 1 || r.Failed != 0 {
				t.Fatalf("identity counts: %+v %v", r.Items, err)
			}
			ids := make(map[string]bool)
			for _, p := range r.Nodes {
				ids[p["uuid"].(string)] = true
			}
			if !ids["UserA"] || !ids["usera"] || !ids[canonical] {
				t.Fatal("import changed authentication IDs")
			}
			if utils.UUIDMap("UserA") == utils.UUIDMap("usera") {
				t.Fatal("fixture no longer distinguishes upstream custom identities")
			}
		})
	}
}

func TestImportYAMLIsolatesInvalidItems(t *testing.T) {
	input := `proxies:
  - {type: ss, server: localhost, port: 1234, cipher: aes-128-gcm, password: private-pass}
  - private-invalid-item
  - null
  - [private-invalid-list]
  - {type: ss, type: vless, password: private-invalid-duplicate-key}
  - {type: vless, server: localhost, port: 1234, uuid: UserA}
`
	r, err := Import([]byte(input))
	if err != nil || r.Accepted != 2 || r.Failed != 4 || len(r.Items) != 6 {
		t.Fatalf("partial YAML report: %+v %v", r.Items, err)
	}
	for i, item := range r.Items {
		want := "failed"
		if i == 0 || i == 5 {
			want = "accepted"
		}
		if item.Index != i+1 || item.Status != want {
			t.Fatalf("wrong item position/status: %+v", item)
		}
	}
	public, _ := json.Marshal(r)
	if strings.Contains(string(public), "private") || strings.Contains(string(public), "UserA") {
		t.Fatal("invalid YAML item leaked into diagnostics")
	}
	for _, input := range []string{"proxies: [", "proxies: '" + basicSS + "'", "proxies: {}", "proxies: []\nproxies: []", "proxies:\n" + strings.Repeat("  - invalid\n", MaxImportNodes+1)} {
		if _, err := Import([]byte(input)); err == nil {
			t.Fatal("invalid YAML document accepted")
		}
	}
}
