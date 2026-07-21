package wisdev

import (
	"os"
	"testing"
)

// TestMain pins this package to a hermetic LLM environment. Tests construct
// real llm.Clients through production constructors — NewAgentGateway wires
// GlobalLLMClient from ambient configuration — and with developer gcloud
// credentials or a local WISDEV_LLM_BASE_URL those clients silently make
// live model calls: nondeterministic assertions (e.g. the PRM judge scoring
// synthetic fixtures), real quota burn, and minutes of extra runtime. CI has
// none of this configuration, so neutralizing it here also keeps local runs
// equivalent to CI. Individual tests can still opt into a provider with
// t.Setenv plus an httptest fake.
func TestMain(m *testing.M) {
	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "wisdev-hermetic-tests-no-such-file.json")
	os.Setenv("GOOGLE_CLOUD_PROJECT", "")
	os.Setenv("GOOGLE_API_KEY", "")
	os.Setenv("GEMINI_API_KEY", "")
	os.Setenv("WISDEV_LLM_PROVIDER", "")
	os.Setenv("WISDEV_LLM_BASE_URL", "")
	os.Exit(m.Run())
}
