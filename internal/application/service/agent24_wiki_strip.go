// Agent24 fork product patch A24-KB-36 (KB-C2-01c3, WeKnora#133, READONLY-v1 R2.6, R2.8).

package service

// Agent24StripWikiInlineChunkCitations removes inline chunk-citation marks.
// Handlers call this wrapper. The regular expression stays in this package.
func Agent24StripWikiInlineChunkCitations(s string) string {
	return stripWikiInlineChunkCitations(s)
}
