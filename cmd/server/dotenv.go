// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G3): a managed host loads no dotenv file.

package main

import "github.com/Tencent/WeKnora/internal/envfile"

// loadDotenv loads dotenv files before any container provider reads the environment. A managed host
// loads none: not .env.lite, not ENV_FILE. The host passes its environment explicitly, and a
// .env.lite shipped next to the binary may carry secrets.
func loadDotenv(managed, liteBuild bool) error {
	if managed {
		return nil
	}
	return envfile.Load(liteBuild)
}
