// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W5.1, W5.2): the format and size
// checks a caller of the internal ingest entry makes before Prepare, so a refused submission saves nothing and
// creates no job (invalid_request/unsupported_format, invalid_request/too_large).

package service

import "strings"

// Agent24IngestMaxBytes is the largest original the entry takes in M3. Knowledge entries are short Markdown; the
// limit is far below upstream's upload limit (MAX_FILE_SIZE_MB, 50 MB by default).
const Agent24IngestMaxBytes = 10 << 20

// agent24IngestFormats are the file types that produce v1 text chunks (READONLY R3.8) and that M3 needs:
// Markdown for knowledge entries, plain text for the probe.
var agent24IngestFormats = map[string]bool{"md": true, "markdown": true, "txt": true}

// Agent24IngestFormatAllowed reports whether fileName's type is in the M3 format allowlist (W5.2).
// A name with nothing before its extension (".md") is refused.
func Agent24IngestFormatAllowed(fileName string) bool {
	dot := strings.LastIndex(fileName, ".")
	if dot <= 0 || strings.HasSuffix(fileName[:dot], "/") {
		return false
	}
	return agent24IngestFormats[strings.ToLower(fileName[dot+1:])]
}

// Agent24IngestTooLarge reports whether an original of n bytes is over Agent24IngestMaxBytes (W5.2).
func Agent24IngestTooLarge(n int) bool { return n > Agent24IngestMaxBytes }
