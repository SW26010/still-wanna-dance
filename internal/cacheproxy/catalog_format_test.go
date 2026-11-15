package cacheproxy

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestCatalogFieldAdaptation(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		udon         bool
		want         CatalogSong
	}{
		{"missing", ``, true, CatalogSong{}},
		{"null", `,"name":null,"playerCount":null,"flip":null,"double_width":null,"skip_random":null,"disable_public":null,"shader_motion":null`, true, CatalogSong{}},
		{"backup null", `,"doubleWidth":null,"skipRandom":null,"disablePublic":null,"shaderMotion":null`, false, CatalogSong{}},
		{"udon values", `,"name":"","playerCount":0,"volume":0,"flip":false,"double_width":true,"skip_random":0,"disable_public":1,"tag":null,"shader_motion":""`, true, CatalogSong{Name: new(string), PlayerCount: new(int64), Volume: new(float64), Flip: new(bool), DoubleWidth: catalogTestBool(true), SkipRandom: catalogTestBool(false), DisablePublic: catalogTestBool(true), Tags: []byte("[]"), ShaderMotion: []byte("[]")}},
		{"backup values", `,"doubleWidth":true,"skipRandom":false,"disablePublic":true,"tag":null,"shaderMotion":[]`, false, CatalogSong{DoubleWidth: catalogTestBool(true), SkipRandom: catalogTestBool(false), DisablePublic: catalogTestBool(true), Tags: []byte("[]"), ShaderMotion: []byte("[]")}},
		{"udon ignores backup fields", `,"doubleWidth":1,"skipRandom":"bad","disablePublic":[],"shaderMotion":{}`, true, CatalogSong{}},
		{"backup ignores udon fields", `,"double_width":1,"skip_random":"bad","disable_public":[],"shader_motion":{}`, false, CatalogSong{}},
		{"array numbers", `,"tag":[9007199254740993,{"x":1}],"shader_motion":[[1,2]]`, true, CatalogSong{Tags: []byte(`[9007199254740993,{"x":1}]`), ShaderMotion: []byte(`[[1,2]]`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			song := `{"id":1,"checksum":"0123456789ABCDEF0123456789ABCDEF","groupName":"child","major":"child"` + tc.fields + `}`
			body := `{"time":"20261115000000","groups":{"contents":[{"groupName":"parent","major":"parent","songInfos":[` + song + `]}]}}`
			if !tc.udon {
				body = `{"code":200,"data":{"time":"20261115000000","groups":[{"title":"parent","major":"parent","entries":[` + song + `]}]}}`
				tc.want.MD5 = "0123456789abcdef0123456789abcdef"
			}
			parent := "parent"
			tc.want.ID, tc.want.GroupName, tc.want.Major = 1, &parent, &parent
			got, err := ParseCatalog([]byte(body), "test")
			if err != nil || len(got.Songs) != 1 || !reflect.DeepEqual(got.Songs[0], tc.want) {
				t.Fatalf("songs=%+v want=%+v err=%v", got.Songs, tc.want, err)
			}
		})
	}
}

func catalogTestBool(value bool) *bool { return &value }

func TestCatalogIdentityErrorsPrecedeMetadataErrors(t *testing.T) {
	body := `{"time":"20261115000000","groups":{"contents":[{"songInfos":[{"id":1,"volume":"bad"},{"id":1}]}]}}`
	if _, err := ParseCatalog([]byte(body), "test"); !errors.Is(err, ErrInvalidCatalogMapping) {
		t.Fatal("metadata failure masked a duplicate identity", err)
	}
}

const catalogSample = `{"code":200,"data":{"time":"20261003235746","groups":[{"title":"Hide","major":"","entries":[{"id":90000,"name":"测试","artist":"测试","dancer":"测试","playerCount":1,"volume":1.0,"start":0,"end":8,"flip":true,"doubleWidth":false,"skipRandom":true,"disablePublic":true,"rpe":100,"genre":"","group":"Hide","composedTitle":"测试 - 测试 | 测试","composedTitleSpell":"c s - c s | c s","ayaId":null,"tag":[],"originalUrl":["https://www.bilibili.com/video/BV1Ds4y1Z7jp/?p=1"],"shaderMotion":[],"checksum":"0e49fdc94f704ed9520fa23a45b20f94"}]}]}}`

func TestCatalogRejectsInvalidCandidates(t *testing.T) {
	cases := []string{
		`{}`, `{"code":200,"data":{"time":"20261003235746","groups":[]}}`,
		strings.Replace(catalogSample, `"code":200`, `"code":500`, 1),
		strings.Replace(catalogSample, `"entries":[`, `"missing":[`, 1),
		strings.Replace(catalogSample, `"id":90000`, `"id":0`, 1),
		strings.Replace(catalogSample, `"tag":[]`, `"tag":{}`, 1),
		strings.Replace(catalogSample, `"flip":true`, `"flip":2`, 1),
		strings.Replace(catalogSample, `"doubleWidth":false`, `"doubleWidth":0`, 1),
		strings.Replace(catalogSample, `"skipRandom":true`, `"skipRandom":1`, 1),
		strings.Replace(catalogSample, `"disablePublic":true`, `"disablePublic":[]`, 1),
		strings.Replace(catalogSample, `"shaderMotion":[]`, `"shaderMotion":""`, 1),
		strings.Replace(catalogSample, `"volume":1.0`, `"volume":"1"`, 1),
		strings.Replace(catalogSample, `"volume":1.0`, `"volume":1e40`, 1),
		strings.Replace(catalogSample, `"volume":1.0`, `"volume":-1e40`, 1),
		strings.Replace(catalogSample, `"20261003235746"`, `""`, 1),
		strings.Replace(catalogSample, `0e49fdc94f704ed9520fa23a45b20f94`, `invalid`, 1),
	}
	for _, body := range cases {
		if _, err := ParseCatalog([]byte(body), "test"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	c, err := ParseCatalog([]byte(catalogSample), "test")
	if err != nil {
		t.Fatal(err)
	}
	c.Songs = append(c.Songs, c.Songs[0])
	if _, err := normalizeCatalog(c); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

func TestCatalogRejectsNonFiniteVolume(t *testing.T) {
	for _, volume := range []float64{math.MaxFloat64, math.Inf(1), math.Inf(-1), math.NaN()} {
		c := testCandidate("20261001000000", CatalogSong{ID: 1, Volume: &volume})
		if _, err := normalizeCatalog(c); err == nil {
			t.Fatalf("accepted volume %v", volume)
		}
	}
}

func TestCatalogIgnoresBackupOnlyAyaID(t *testing.T) {
	for _, value := range []string{`"external-42"`, `"12"`, `""`, `true`, `[]`, `{}`} {
		body := strings.Replace(catalogSample, `"ayaId":null`, `"ayaId":`+value, 1)
		if _, err := ParseCatalog([]byte(body), "test"); err != nil {
			t.Fatalf("unused ayaId affected parsing %s: %v", value, err)
		}
	}
}

func TestCatalogRejectsInvalidTimes(t *testing.T) {
	for _, revision := range []string{"release-42", "9", "10", "2026100323574x", "202610032357460", "20260230010101", "20261301000000", "20261004246000"} {
		if _, err := ParseCatalog([]byte(strings.Replace(catalogSample, "20261003235746", revision, 1)), "test"); err == nil {
			t.Fatalf("accepted unsupported revision %q", revision)
		}
	}
}

func TestCatalogTimesFromExistingEndpoints(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"20261004235822", "2026-10-04 23:58:22"}, // Kiva data.time and Udon time.
		{"20260215004959", "2026-02-15 00:49:59"}, // WannaInfo data.time.
		{"20240229235959", "2024-02-29 23:59:59"}, // Calendar validation permits leap day.
	} {
		got, err := ParseCatalogTime(tc.value)
		if err != nil || got.Format("2006-01-02 15:04:05") != tc.want {
			t.Fatal(tc.value, got, err)
		}
	}
	for _, revision := range []string{"20261004235822.000", "20261004235822,123", "20261004235822Z"} {
		if _, err := ParseCatalogTime(revision); err == nil {
			t.Fatal("accepted noncanonical civil revision", revision)
		}
	}
}
