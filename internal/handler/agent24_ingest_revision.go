// Agent24 fork product patch A24-KB-41 (WeKnora#136): the source_revision callers of the internal ingest entry
// record on a job, derived exactly as the read and search entries report it (READONLY R5.1).

package handler

import "github.com/Tencent/WeKnora/internal/types"

// Agent24IngestSourceRevision returns the revision derivation for Agent24Ingest.Stage under key (the integration
// tag key, Agent24KnowledgeTagKeyFromEnv).
func Agent24IngestSourceRevision(key []byte) func(*types.Knowledge) string {
	return func(k *types.Knowledge) string {
		mv, err := agent24ManualVersion(k)
		if err != nil {
			mv = 1
		}
		return agent24SourceRevision(key, k.ID, agent24SourceFingerprint{
			FileHash: k.FileHash, FilePath: k.FilePath, Source: k.Source, ManualVersion: mv,
		})
	}
}
