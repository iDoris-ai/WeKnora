package datasource

import (
	"testing"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func TestValidateConnectorBaseURLBlocksLoopback(t *testing.T) {
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	err := ValidateConnectorBaseURL("http://127.0.0.1:8000")
	if err == nil {
		t.Fatal("expected loopback base_url to be rejected")
	}
}

func TestValidateConnectorBaseURLAllowsPublicHTTPS(t *testing.T) {
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	if err := ValidateConnectorBaseURL("https://open.feishu.cn"); err != nil {
		t.Fatalf("expected public base_url to pass: %v", err)
	}
}

func TestConnectorBaseURLForLogDropsUserinfoAndQuery(t *testing.T) {
	cases := map[string]string{
		"https://user:S3NT1NEL@yuque.example.com/": "https://yuque.example.com",
		"https://ima.example.com/?token=S3NT1NEL":  "https://ima.example.com",
		"http://127.0.0.1:8080/api#S3NT1NEL":       "http://127.0.0.1:8080",
		"://bad S3NT1NEL":                          "(unparsed len=15)",
		"":                                         "(unparsed len=0)",
	}
	for raw, want := range cases {
		if got := ConnectorBaseURLForLog(raw); got != want {
			t.Errorf("ConnectorBaseURLForLog(%q) = %q, want %q", raw, got, want)
		}
	}
}
