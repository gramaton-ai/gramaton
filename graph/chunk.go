package graph

// ChunkText splits text into overlapping chunks based on a token budget.
// Uses a simple approximation of 1 token per 4 characters.
// Returns nil if the text is at or below the threshold.
func ChunkText(text string, thresholdTokens, chunkTokens, overlapTokens int) []string {
	charsPerToken := 4
	thresholdChars := thresholdTokens * charsPerToken
	chunkChars := chunkTokens * charsPerToken
	overlapChars := overlapTokens * charsPerToken

	if len(text) <= thresholdChars {
		return nil
	}

	var chunks []string
	start := 0
	for start < len(text) {
		end := start + chunkChars
		if end > len(text) {
			end = len(text)
		}

		// Try to break at a word boundary.
		if end < len(text) {
			boundary := end
			minBound := end - chunkChars/5
			if minBound < start {
				minBound = start
			}
			for boundary > minBound {
				if text[boundary] == ' ' || text[boundary] == '\n' {
					end = boundary
					break
				}
				boundary--
			}
		}

		chunks = append(chunks, text[start:end])

		// Advance based on actual chunk end, minus overlap.
		nextStart := end - overlapChars
		if nextStart <= start {
			nextStart = start + chunkChars/2
		}
		start = nextStart

		// If the remaining text is less than half a chunk, include it
		// in the last chunk rather than creating a tiny fragment.
		if start < len(text) && len(text)-start < chunkChars/2 {
			chunks = append(chunks, text[start:])
			break
		}
	}

	return chunks
}
