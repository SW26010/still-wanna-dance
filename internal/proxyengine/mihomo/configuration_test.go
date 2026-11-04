package mihomo

import (
	"bytes"
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
)

const configurationFixture = `# Preserve original names, DNS and policy references.
proxies:
  - {name: first, type: ss, server: localhost, port: 1234, cipher: aes-128-gcm, password: private}
proxy-groups:
  - {name: original, type: select, proxies: [first, DIRECT]}
dns:
  enable: true
  nameserver: ['https://1.1.1.1/dns-query#original']
  proxy-server-nameserver: [223.5.5.5]
hosts: {proxy.invalid: 127.0.0.1}
rules: ['MATCH,original']
`

func TestManagedConfigurationPreservesDependenciesAndOverridesIntegration(t *testing.T) {
	input := []byte(configurationFixture + `
port: 29991
mixed-port: 29992
external-controller: 0.0.0.0:29993
external-ui: private-directory
listeners: [{name: private-listener, type: socks, port: 29994}]
tun: {enable: true, auto-route: true}
ntp: {enable: true}
geo-auto-update: true
`)
	before := bytes.Clone(input)
	original, err := readConfiguration(input)
	if err != nil {
		t.Fatal(err)
	}
	managed, entry, err := managedConfiguration(input, true)
	if err != nil {
		t.Fatal(err)
	}
	if entry != ProxyGroup || !reflect.DeepEqual(managed.Proxy, original.Proxy) || !reflect.DeepEqual(managed.ProxyGroup[2:], original.ProxyGroup) || !reflect.DeepEqual(managed.Rule, original.Rule) || !reflect.DeepEqual(managed.Hosts, original.Hosts) || !reflect.DeepEqual(managed.DNS.NameServer, original.DNS.NameServer) || !reflect.DeepEqual(managed.DNS.ProxyServerNameserver, original.DNS.ProxyServerNameserver) {
		t.Fatal("original connection semantics lost")
	}
	if managed.Port != 0 || managed.MixedPort != 0 || managed.ExternalController != "" || managed.ExternalUI != "" || len(managed.Listeners) != 0 || managed.Tun.Enable || managed.NTP.Enable || managed.GeoAutoUpdate || managed.DNS.Listen != "" || managed.DNS.UseSystemHosts {
		t.Fatal("integration settings escaped override")
	}
	if !reflect.DeepEqual(managed.ProxyGroup[0]["proxies"], []string{automaticGroup, "original"}) || !reflect.DeepEqual(managed.ProxyGroup[1]["proxies"], []string{"original"}) {
		t.Fatal("SWD expanded original groups into nodes")
	}
	again, _, err := managedConfiguration(input, true)
	if err != nil || len(again.ProxyGroup) != len(managed.ProxyGroup) || !bytes.Equal(input, before) {
		t.Fatal("override accumulated or changed original bytes")
	}
}

func TestConfigurationNativeValidationIsolatedAndPrivate(t *testing.T) {
	before, hosts := resolver.DefaultResolver, resolver.UseSystemHosts
	r, err := Validate(context.Background(), []byte(configurationFixture), t.TempDir())
	if err != nil || r.Nodes != 1 || r.Groups != 1 {
		t.Fatal("native configuration rejected", err)
	}
	if resolver.DefaultResolver != before || resolver.UseSystemHosts != hosts {
		t.Fatal("checking modified running DNS")
	}
	for _, input := range []string{
		strings.Replace(configurationFixture, "type: ss", "type: private-invalid", 1),
		strings.Replace(configurationFixture, "MATCH,original", "MATCH,missing-private-group", 1),
	} {
		if _, err := Validate(context.Background(), []byte(input), t.TempDir()); err != ErrConfiguration {
			t.Fatal("native rejection was not private", err)
		}
	}
}

func TestConfigurationRejectsNodeListsAndLegacySnapshots(t *testing.T) {
	uri := "ss://YWVzLTEyOC1nY206cHJpdmF0ZQ@localhost:1234"
	for _, input := range []string{"", uri, base64.StdEncoding.EncodeToString([]byte(uri)), `{"version":2,"nodes":[]}`, "- {name: first, type: ss}", strings.Repeat("x", MaxImportBytes+1), "hosts:\n  :\n  :\n"} {
		if _, err := Inspect([]byte(input)); err == nil {
			t.Fatal("non-configuration accepted")
		}
	}
}

func TestConfigurationGroupNameCollisionAndProviderOnly(t *testing.T) {
	input := strings.Replace(configurationFixture, "name: original", "name: SWD", 1)
	input = strings.ReplaceAll(input, "#original", "#SWD")
	input = strings.ReplaceAll(input, "MATCH,original", "MATCH,SWD")
	raw, entry, err := managedConfiguration([]byte(input), true)
	if err != nil || entry != "SWD (1)" || raw.ProxyGroup[2]["name"] != "SWD" {
		t.Fatal("original group overwritten", err)
	}
	provider := []byte("proxy-providers: {source: {type: http, url: 'https://example.invalid/nodes.yaml', interval: 3600}}")
	raw, _, err = managedConfiguration(provider, true)
	if err != nil || raw.ProxyGroup[0]["include-all"] != true || raw.ProxyGroup[1]["include-all"] != true {
		t.Fatal("provider-only configuration has no default exit", err)
	}
	raw, _, err = managedConfiguration([]byte(configurationFixture+"profile: {store-selected: false}\n"), true)
	if err != nil || raw.Profile.StoreSelected {
		t.Fatal("imported native cache preference was overridden", err)
	}
}
